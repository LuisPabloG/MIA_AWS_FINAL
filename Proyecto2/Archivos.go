package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// REMOVE: Elimina un archivo o carpeta en la ruta especificada.
func remove(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	if !hasPath || !hasID {
		return "Error: Parámetros -path y -id son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	parentInodeIndex, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Verificar permisos de escritura en la carpeta padre
	parentInode, err := readInode(file, sb, parentInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo padre: %v", err)
	}
	if !hasWritePermission(parentInode, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para eliminar en la carpeta padre"
	}

	// Buscar el elemento a eliminar
	var targetInodeIndex int32 = -1
	var targetBlockIndex int32
	var targetContentIndex int
	found := false
	for _, blockIndex := range parentInode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for i, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				targetInodeIndex = content.BInode
				targetBlockIndex = blockIndex
				targetContentIndex = i
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return fmt.Sprintf("Error: %s no encontrado", fileName)
	}

	// Leer inodo del elemento
	targetInode, err := readInode(file, sb, targetInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo %d: %v", targetInodeIndex, err)
	}

	// Verificar permisos de escritura en el elemento
	if !hasWritePermission(targetInode, currentSession.UserID, currentSession.GroupID) {
		return fmt.Sprintf("Error: Permisos insuficientes para eliminar %s", fileName)
	}

	// Leer bitmaps
	bitmapInodes := make([]byte, sb.SInodesCount)
	bitmapBlocks := make([]byte, sb.SBlocksCount)
	file.Seek(int64(sb.SBmInodeStart), 0)
	_, err = file.Read(bitmapInodes)
	if err != nil {
		return fmt.Sprintf("Error al leer bitmap de inodos: %v", err)
	}
	file.Seek(int64(sb.SBmBlockStart), 0)
	_, err = file.Read(bitmapBlocks)
	if err != nil {
		return fmt.Sprintf("Error al leer bitmap de bloques: %v", err)
	}

	// Liberar recursos
	freedBlocks := int32(0)
	if targetInode.IType == '0' {
		// Carpeta: Verificar si está vacía (excepto . y ..)
		for _, blockIndex := range targetInode.IBlock {
			if blockIndex == -1 {
				continue
			}
			folderBlock, err := readFolderBlock(file, sb, blockIndex)
			if err != nil {
				return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
			}
			for _, content := range folderBlock.BContent {
				name := strings.Trim(string(content.BName[:]), "\x00")
				if name != "" && name != "." && name != ".." {
					return fmt.Sprintf("Error: La carpeta %s no está vacía", fileName)
				}
			}
			bitmapBlocks[blockIndex] = 0
			freedBlocks++
		}
	} else if targetInode.IType == '1' {
		// Archivo: Liberar bloques
		for _, blockIndex := range targetInode.IBlock {
			if blockIndex == -1 {
				continue
			}
			bitmapBlocks[blockIndex] = 0
			freedBlocks++
		}
	}

	// Liberar inodo
	bitmapInodes[targetInodeIndex] = 0

	// Actualizar carpeta padre
	folderBlock, err := readFolderBlock(file, sb, targetBlockIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer bloque %d: %v", targetBlockIndex, err)
	}
	folderBlock.BContent[targetContentIndex] = struct {
		BName  [12]byte
		BInode int32
	}{}
	file.Seek(int64(sb.SBlockStart+targetBlockIndex*sb.SBlockSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
		return fmt.Sprintf("Error al actualizar bloque %d: %v", targetBlockIndex, err)
	}

	// Escribir bitmaps
	file.Seek(int64(sb.SBmInodeStart), 0)
	if _, err = file.Write(bitmapInodes); err != nil {
		return fmt.Sprintf("Error al escribir bitmap de inodos: %v", err)
	}
	file.Seek(int64(sb.SBmBlockStart), 0)
	if _, err = file.Write(bitmapBlocks); err != nil {
		return fmt.Sprintf("Error al escribir bitmap de bloques: %v", err)
	}

	// Actualizar superbloque
	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}
	var partStart int32
	foundPart := false
	for _, p := range mbr.MbrPartitions {
		name := strings.Trim(string(p.PartName[:]), "\x00")
		if name == mp.Name {
			partStart = p.PartStart
			foundPart = true
			break
		}
	}
	if !foundPart {
		for _, p := range mbr.MbrPartitions {
			if p.PartType == 'E' {
				currentPos := p.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err = binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						continue
					}
					name := strings.Trim(string(ebr.PartName[:]), "\x00")
					if name == mp.Name && ebr.PartSize > 0 {
						partStart = ebr.PartStart
						foundPart = true
						break
					}
					currentPos = ebr.PartNext
				}
			}
			if foundPart {
				break
			}
		}
	}
	if !foundPart {
		return fmt.Sprintf("Error: Partición %s no encontrada en MBR ni EBR", mp.Name)
	}

	file.Seek(int64(partStart), 0)
	var sbUpdated Superblock
	if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}
	sbUpdated.SFreeInodesCount++
	sbUpdated.SFreeBlocksCount += freedBlocks
	file.Seek(int64(partStart), 0)
	if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("%s eliminado exitosamente", fileName)
}

// COPY: Copia un archivo o carpeta a una nueva ubicación.
func copyMap(params map[string]string) string {
	src, hasSrc := params["path"]
	dest, hasDest := params["dest"]
	id, hasID := params["id"]
	if !hasSrc || !hasDest || !hasID {
		return "Error: Parámetros -path, -dest y -id son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar rutas
	srcParts, err := normalizePath(src)
	if err != nil {
		return fmt.Sprintf("Error en ruta fuente: %v", err)
	}
	destParts, err := normalizePath(dest)
	if err != nil {
		return fmt.Sprintf("Error en ruta destino: %v", err)
	}

	// Navegar a la carpeta padre del origen
	srcFileName := srcParts[len(srcParts)-1]
	srcParentPath := srcParts[:len(srcParts)-1]
	srcParentInode, err := navigateToParent(file, sb, srcParentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre del origen: %v", err)
	}

	// Encontrar el inodo del origen
	var srcInodeIndex int32 = -1
	for _, blockIndex := range (func() Inode {
		inode, _ := readInode(file, sb, srcParentInode)
		return inode
	})().IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == srcFileName {
				srcInodeIndex = content.BInode
				break
			}
		}
		if srcInodeIndex != -1 {
			break
		}
	}
	if srcInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", srcFileName)
	}

	srcInode, err := readInode(file, sb, srcInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo %d: %v", srcInodeIndex, err)
	}

	// Navegar a la carpeta destino
	destParentInode, err := navigateToParent(file, sb, destParts[:len(destParts)-1])
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta destino: %v", err)
	}

	// Verificar permisos
	if !hasWritePermission(srcInode, currentSession.UserID, currentSession.GroupID) {
		return fmt.Sprintf("Error: Permisos insuficientes para leer %s", srcFileName)
	}
	destParentInodeData, err := readInode(file, sb, destParentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo destino: %v", err)
	}
	if !hasWritePermission(destParentInodeData, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para escribir en la carpeta destino"
	}

	// Verificar si el destino ya existe
	destFileName := destParts[len(destParts)-1]
	for _, blockIndex := range destParentInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == destFileName {
				return fmt.Sprintf("Error: %s ya existe en la ruta destino", destFileName)
			}
		}
	}

	// Copiar archivo
	if srcInode.IType == '1' {
		// Leer contenido
		var content strings.Builder
		for _, blockIndex := range srcInode.IBlock {
			if blockIndex == -1 {
				continue
			}
			fileBlock, err := readFileBlock(file, sb, blockIndex)
			if err != nil {
				return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
			}
			content.Write(fileBlock.BContent[:])
		}
		contentStr := strings.Trim(content.String(), "\x00")

		// Crear nuevo archivo en destino
		newParams := map[string]string{
			"path": dest,
			"cont": contentStr,
			"id":   id,
		}
		return mkfile(newParams)
	}

	return "Error: Copia de carpetas no implementada"
}

// MOVE: Mueve un archivo o carpeta a una nueva ubicación.
func move(params map[string]string) string {
	src, hasSrc := params["path"]
	dest, hasDest := params["dest"]
	id, hasID := params["id"]
	if !hasSrc || !hasDest || !hasID {
		return "Error: Parámetros -path, -dest y -id son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar rutas
	srcParts, err := normalizePath(src)
	if err != nil {
		return fmt.Sprintf("Error en ruta fuente: %v", err)
	}
	destParts, err := normalizePath(dest)
	if err != nil {
		return fmt.Sprintf("Error en ruta destino: %v", err)
	}

	// Navegar a la carpeta padre del origen
	srcFileName := srcParts[len(srcParts)-1]
	srcParentPath := srcParts[:len(srcParts)-1]
	srcParentInode, err := navigateToParent(file, sb, srcParentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre del origen: %v", err)
	}

	// Encontrar el inodo del origen
	var srcInodeIndex int32 = -1
	var srcBlockIndex int32
	var srcContentIndex int
	for _, blockIndex := range (func() Inode {
		inode, _ := readInode(file, sb, srcParentInode)
		return inode
	})().IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for i, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == srcFileName {
				srcInodeIndex = content.BInode
				srcBlockIndex = blockIndex
				srcContentIndex = i
				break
			}
		}
		if srcInodeIndex != -1 {
			break
		}
	}
	if srcInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", srcFileName)
	}

	srcInode, err := readInode(file, sb, srcInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo %d: %v", srcInodeIndex, err)
	}

	// Navegar a la carpeta destino
	destParentInode, err := navigateToParent(file, sb, destParts[:len(destParts)-1])
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta destino: %v", err)
	}

	// Verificar permisos
	if !hasWritePermission(srcInode, currentSession.UserID, currentSession.GroupID) {
		return fmt.Sprintf("Error: Permisos insuficientes para mover %s", srcFileName)
	}
	destParentInodeData, err := readInode(file, sb, destParentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo destino: %v", err)
	}
	if !hasWritePermission(destParentInodeData, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para escribir en la carpeta destino"
	}

	// Verificar si el destino ya existe
	destFileName := destParts[len(destParts)-1]
	for _, blockIndex := range destParentInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == destFileName {
				return fmt.Sprintf("Error: %s ya existe en la ruta destino", destFileName)
			}
		}
	}

	// Actualizar carpeta padre origen
	folderBlock, err := readFolderBlock(file, sb, srcBlockIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer bloque %d: %v", srcBlockIndex, err)
	}
	folderBlock.BContent[srcContentIndex] = struct {
		BName  [12]byte
		BInode int32
	}{}
	file.Seek(int64(sb.SBlockStart+srcBlockIndex*sb.SBlockSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
		return fmt.Sprintf("Error al actualizar bloque %d: %v", srcBlockIndex, err)
	}

	// Añadir a la carpeta destino
	found := false
	for _, blockIndex := range destParentInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err = readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for i := range folderBlock.BContent {
			name := strings.Trim(string(folderBlock.BContent[i].BName[:]), "\x00")
			if name == "" {
				copy(folderBlock.BContent[i].BName[:], []byte(destFileName))
				folderBlock.BContent[i].BInode = srcInodeIndex
				file.Seek(int64(sb.SBlockStart+blockIndex*sb.SBlockSize), 0)
				if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
					return fmt.Sprintf("Error al actualizar bloque %d: %v", blockIndex, err)
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		// Asignar nuevo bloque a la carpeta destino
		bitmapBlocks := make([]byte, sb.SBlocksCount)
		file.Seek(int64(sb.SBmBlockStart), 0)
		_, err = file.Read(bitmapBlocks)
		if err != nil {
			return fmt.Sprintf("Error al leer bitmap de bloques: %v", err)
		}
		currentBlock := int32(2)
		for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
			currentBlock++
		}
		if currentBlock >= sb.SBlocksCount {
			return "Error: No hay bloques libres para la carpeta destino"
		}
		newBlockIndex := currentBlock
		bitmapBlocks[currentBlock] = 1

		var newFolderBlock FolderBlock
		copy(newFolderBlock.BContent[0].BName[:], []byte(destFileName))
		newFolderBlock.BContent[0].BInode = srcInodeIndex
		file.Seek(int64(sb.SBlockStart+newBlockIndex*sb.SBlockSize), 0)
		if err = binary.Write(file, binary.LittleEndian, &newFolderBlock); err != nil {
			return fmt.Sprintf("Error al escribir bloque %d: %v", newBlockIndex, err)
		}

		// Actualizar inodo destino
		for i := range destParentInodeData.IBlock {
			if destParentInodeData.IBlock[i] == -1 {
				destParentInodeData.IBlock[i] = newBlockIndex
				break
			}
		}
		file.Seek(int64(sb.SInodeStart+destParentInode*sb.SInodeSize), 0)
		if err = binary.Write(file, binary.LittleEndian, &destParentInodeData); err != nil {
			return fmt.Sprintf("Error al actualizar inodo %d: %v", destParentInode, err)
		}

		// Escribir bitmap
		file.Seek(int64(sb.SBmBlockStart), 0)
		if _, err = file.Write(bitmapBlocks); err != nil {
			return fmt.Sprintf("Error al escribir bitmap de bloques: %v", err)
		}

		// Actualizar superbloque
		mbr, err := readMBR(file)
		if err != nil {
			return fmt.Sprintf("Error al leer MBR: %v", err)
		}
		var partStart int32
		foundPart := false
		for _, p := range mbr.MbrPartitions {
			name := strings.Trim(string(p.PartName[:]), "\x00")
			if name == mp.Name {
				partStart = p.PartStart
				foundPart = true
				break
			}
		}
		if !foundPart {
			for _, p := range mbr.MbrPartitions {
				if p.PartType == 'E' {
					currentPos := p.PartStart
					for currentPos != -1 {
						file.Seek(int64(currentPos), 0)
						var ebr EBR
						if err = binary.Read(file, binary.LittleEndian, &ebr); err != nil {
							continue
						}
						name := strings.Trim(string(ebr.PartName[:]), "\x00")
						if name == mp.Name && ebr.PartSize > 0 {
							partStart = ebr.PartStart
							foundPart = true
							break
						}
						currentPos = ebr.PartNext
					}
				}
				if foundPart {
					break
				}
			}
		}
		if !foundPart {
			return fmt.Sprintf("Error: Partición %s no encontrada en MBR ni EBR", mp.Name)
		}

		file.Seek(int64(partStart), 0)
		var sbUpdated Superblock
		if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
			return fmt.Sprintf("Error al leer superbloque: %v", err)
		}
		sbUpdated.SFreeBlocksCount--
		file.Seek(int64(partStart), 0)
		if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
			return fmt.Sprintf("Error al escribir superbloque: %v", err)
		}
	}

	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("%s movido exitosamente a %s", srcFileName, dest)
}

// FIND: Busca archivos o carpetas que coincidan con un patrón.
func find(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	name, hasName := params["name"]
	if !hasPath || !hasID || !hasName {
		return "Error: Parámetros -path, -id y -name son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	// Navegar hasta la carpeta inicial
	currentInode, err := navigateToParent(file, sb, pathParts)
	if err != nil {
		return fmt.Sprintf("Error al navegar a %s: %v", path, err)
	}

	// Buscar recursivamente
	var results []string
	err = findRecursive(file, sb, currentInode, name, path, &results)
	if err != nil {
		return fmt.Sprintf("Error durante la búsqueda: %v", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No se encontraron coincidencias para %s", name)
	}

	return strings.Join(results, "\n")
}

// findRecursive: Función auxiliar para buscar recursivamente.
func findRecursive(file *os.File, sb Superblock, inodeIndex int32, pattern, currentPath string, results *[]string) error {
	inode, err := readInode(file, sb, inodeIndex)
	if err != nil {
		return fmt.Errorf("error al leer inodo %d: %v", inodeIndex, err)
	}

	if inode.IType != '0' {
		return nil // No es una carpeta, ignorar
	}

	for _, blockIndex := range inode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Errorf("error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == "" || name == "." || name == ".." {
				continue
			}
			if strings.Contains(name, pattern) {
				*results = append(*results, fmt.Sprintf("%s/%s", currentPath, name))
			}
			if content.BInode != -1 {
				inodeChild, err := readInode(file, sb, content.BInode)
				if err != nil {
					continue
				}
				if inodeChild.IType == '0' {
					err = findRecursive(file, sb, content.BInode, pattern, fmt.Sprintf("%s/%s", currentPath, name), results)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// CHOWN: Cambia el propietario de un archivo o carpeta.
func chown(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	user, hasUser := params["usr"]
	recursive := false
	if r, hasR := params["r"]; hasR && r == "true" {
		recursive = true
	}
	if !hasPath || !hasID || !hasUser {
		return "Error: Parámetros -path, -id y -usr son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede cambiar propietarios"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Obtener UID del usuario
	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}
	var newUID int32 = -1
	for _, line := range strings.Split(usersContent, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 4 && parts[1] == "U" && strings.TrimSpace(parts[3]) == user {
			uid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			newUID = int32(uid)
			break
		}
	}
	if newUID == -1 {
		return fmt.Sprintf("Error: El usuario %s no existe", user)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	parentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Encontrar el inodo
	var targetInodeIndex int32 = -1
	for _, blockIndex := range (func() Inode {
		inode, _ := readInode(file, sb, parentInode)
		return inode
	})().IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				targetInodeIndex = content.BInode
				break
			}
		}
		if targetInodeIndex != -1 {
			break
		}
	}
	if targetInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", fileName)
	}

	// Cambiar propietario
	err = changeOwner(file, sb, targetInodeIndex, newUID, recursive)
	if err != nil {
		return fmt.Sprintf("Error al cambiar propietario: %v", err)
	}

	return fmt.Sprintf("Propietario de %s cambiado a %s exitosamente", fileName, user)
}

// changeOwner: Función auxiliar para cambiar propietario recursivamente.
func changeOwner(file *os.File, sb Superblock, inodeIndex int32, newUID int32, recursive bool) error {
	inode, err := readInode(file, sb, inodeIndex)
	if err != nil {
		return fmt.Errorf("error al leer inodo %d: %v", inodeIndex, err)
	}

	inode.IUid = newUID
	file.Seek(int64(sb.SInodeStart+inodeIndex*sb.SInodeSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &inode); err != nil {
		return fmt.Errorf("error al escribir inodo %d: %v", inodeIndex, err)
	}

	if recursive && inode.IType == '0' {
		for _, blockIndex := range inode.IBlock {
			if blockIndex == -1 {
				continue
			}
			folderBlock, err := readFolderBlock(file, sb, blockIndex)
			if err != nil {
				return fmt.Errorf("error al leer bloque %d: %v", blockIndex, err)
			}
			for _, content := range folderBlock.BContent {
				name := strings.Trim(string(content.BName[:]), "\x00")
				if name == "" || name == "." || name == ".." {
					continue
				}
				if content.BInode != -1 {
					err = changeOwner(file, sb, content.BInode, newUID, true)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// CHMOD: Cambia los permisos de un archivo o carpeta.
func chmod(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	ugo, hasUgo := params["ugo"]
	recursive := false
	if r, hasR := params["r"]; hasR && r == "true" {
		recursive = true
	}
	if !hasPath || !hasID || !hasUgo {
		return "Error: Parámetros -path, -id y -ugo son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede cambiar permisos"
	}

	// Validar ugo
	perm, err := strconv.Atoi(ugo)
	if err != nil || perm < 0 || perm > 777 {
		return "Error: Valor de -ugo inválido"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	parentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Encontrar el inodo
	var targetInodeIndex int32 = -1
	for _, blockIndex := range (func() Inode {
		inode, _ := readInode(file, sb, parentInode)
		return inode
	})().IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				targetInodeIndex = content.BInode
				break
			}
		}
		if targetInodeIndex != -1 {
			break
		}
	}
	if targetInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", fileName)
	}

	// Cambiar permisos
	err = changePermissions(file, sb, targetInodeIndex, int32(perm), recursive)
	if err != nil {
		return fmt.Sprintf("Error al cambiar permisos: %v", err)
	}

	return fmt.Sprintf("Permisos de %s cambiados a %s exitosamente", fileName, ugo)
}

// changePermissions: Función auxiliar para cambiar permisos recursivamente.
func changePermissions(file *os.File, sb Superblock, inodeIndex int32, newPerm int32, recursive bool) error {
	inode, err := readInode(file, sb, inodeIndex)
	if err != nil {
		return fmt.Errorf("error al leer inodo %d: %v", inodeIndex, err)
	}

	inode.IPerm = newPerm
	file.Seek(int64(sb.SInodeStart+inodeIndex*sb.SInodeSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &inode); err != nil {
		return fmt.Errorf("error al escribir inodo %d: %v", inodeIndex, err)
	}

	if recursive && inode.IType == '0' {
		for _, blockIndex := range inode.IBlock {
			if blockIndex == -1 {
				continue
			}
			folderBlock, err := readFolderBlock(file, sb, blockIndex)
			if err != nil {
				return fmt.Errorf("error al leer bloque %d: %v", blockIndex, err)
			}
			for _, content := range folderBlock.BContent {
				name := strings.Trim(string(content.BName[:]), "\x00")
				if name == "" || name == "." || name == ".." {
					continue
				}
				if content.BInode != -1 {
					err = changePermissions(file, sb, content.BInode, newPerm, true)
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// EDIT: Edita el contenido de un archivo.
func edit(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	cont, hasCont := params["cont"]
	if !hasPath || !hasID || !hasCont {
		return "Error: Parámetros -path, -id y -cont son obligatorios"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	parentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Encontrar el inodo del archivo
	var targetInodeIndex int32 = -1
	for _, blockIndex := range (func() Inode {
		inode, _ := readInode(file, sb, parentInode)
		return inode
	})().IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				targetInodeIndex = content.BInode
				break
			}
		}
		if targetInodeIndex != -1 {
			break
		}
	}
	if targetInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", fileName)
	}

	inode, err := readInode(file, sb, targetInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo %d: %v", targetInodeIndex, err)
	}
	if inode.IType != '1' {
		return fmt.Sprintf("Error: %s no es un archivo", fileName)
	}
	if !hasWritePermission(inode, currentSession.UserID, currentSession.GroupID) {
		return fmt.Sprintf("Error: Permisos insuficientes para editar %s", fileName)
	}

	// Leer bitmaps
	bitmapBlocks := make([]byte, sb.SBlocksCount)
	file.Seek(int64(sb.SBmBlockStart), 0)
	_, err = file.Read(bitmapBlocks)
	if err != nil {
		return fmt.Sprintf("Error al leer bitmap de bloques: %v", err)
	}

	// Liberar bloques anteriores
	freedBlocks := int32(0)
	for i, blockIndex := range inode.IBlock {
		if blockIndex != -1 {
			bitmapBlocks[blockIndex] = 0
			inode.IBlock[i] = -1
			freedBlocks++
		}
	}

	// Asignar nuevos bloques
	content := []byte(cont)
	numBlocks := int32(math.Ceil(float64(len(content)) / 64.0))
	if numBlocks > 15 {
		return fmt.Sprintf("Error: El contenido requiere %d bloques, máximo 15", numBlocks)
	}

	newBlocks := make([]int32, numBlocks)
	currentBlock := int32(2)
	for i := int32(0); i < numBlocks; i++ {
		for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
			currentBlock++
		}
		if currentBlock >= sb.SBlocksCount {
			return "Error: No hay bloques libres"
		}
		newBlocks[i] = currentBlock
		bitmapBlocks[currentBlock] = 1
		currentBlock++
	}

	// Escribir nuevos bloques
	for i, blockIndex := range newBlocks {
		var block FileBlock
		start := int32(i * 64)
		end := start + 64
		if end > int32(len(content)) {
			end = int32(len(content))
		}
		copy(block.BContent[:], content[start:end])
		file.Seek(int64(sb.SBlockStart+blockIndex*sb.SBlockSize), 0)
		if err = binary.Write(file, binary.LittleEndian, &block); err != nil {
			return fmt.Sprintf("Error al escribir bloque %d: %v", blockIndex, err)
		}
	}

	// Actualizar inodo
	inode.ISize = int32(len(content))
	for i, blockIndex := range newBlocks {
		inode.IBlock[i] = blockIndex
	}
	fecha := time.Now().Format("2006-01-02 15:04:05")
	copy(inode.IMtime[:], []byte(fecha))
	file.Seek(int64(sb.SInodeStart+targetInodeIndex*sb.SInodeSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &inode); err != nil {
		return fmt.Sprintf("Error al escribir inodo %d: %v", targetInodeIndex, err)
	}

	// Escribir bitmap
	file.Seek(int64(sb.SBmBlockStart), 0)
	if _, err = file.Write(bitmapBlocks); err != nil {
		return fmt.Sprintf("Error al escribir bitmap de bloques: %v", err)
	}

	// Actualizar superbloque
	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}
	var partStart int32
	foundPart := false
	for _, p := range mbr.MbrPartitions {
		name := strings.Trim(string(p.PartName[:]), "\x00")
		if name == mp.Name {
			partStart = p.PartStart
			foundPart = true
			break
		}
	}
	if !foundPart {
		for _, p := range mbr.MbrPartitions {
			if p.PartType == 'E' {
				currentPos := p.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err = binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						continue
					}
					name := strings.Trim(string(ebr.PartName[:]), "\x00")
					if name == mp.Name && ebr.PartSize > 0 {
						partStart = ebr.PartStart
						foundPart = true
						break
					}
					currentPos = ebr.PartNext
				}
			}
			if foundPart {
				break
			}
		}
	}
	if !foundPart {
		return fmt.Sprintf("Error: Partición %s no encontrada en MBR ni EBR", mp.Name)
	}

	file.Seek(int64(partStart), 0)
	var sbUpdated Superblock
	if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}
	sbUpdated.SFreeBlocksCount += freedBlocks - numBlocks
	file.Seek(int64(partStart), 0)
	if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("Archivo %s editado exitosamente", fileName)
}

// RENAME: Renombra un archivo o carpeta.
func rename(params map[string]string) string {
	path, hasPath := params["path"]
	id, hasID := params["id"]
	name, hasName := params["name"]
	if !hasPath || !hasID || !hasName {
		return "Error: Parámetros -path, -id y -name son obligatorios"
	}

	if len(name) > 12 {
		return "Error: El nombre excede 12 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	parentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Verificar permisos
	parentInodeData, err := readInode(file, sb, parentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo padre: %v", err)
	}
	if !hasWritePermission(parentInodeData, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para renombrar en la carpeta padre"
	}

	// Encontrar el inodo
	var targetInodeIndex int32 = -1
	var targetBlockIndex int32
	var targetContentIndex int
	for _, blockIndex := range parentInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for i, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				targetInodeIndex = content.BInode
				targetBlockIndex = blockIndex
				targetContentIndex = i
				break
			}
		}
		if targetInodeIndex != -1 {
			break
		}
	}
	if targetInodeIndex == -1 {
		return fmt.Sprintf("Error: %s no encontrado", fileName)
	}

	// Verificar si el nuevo nombre ya existe
	for _, blockIndex := range parentInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == name {
				return fmt.Sprintf("Error: El nombre %s ya existe", name)
			}
		}
	}

	// Actualizar nombre
	folderBlock, err := readFolderBlock(file, sb, targetBlockIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer bloque %d: %v", targetBlockIndex, err)
	}
	for i := range folderBlock.BContent[targetContentIndex].BName {
		folderBlock.BContent[targetContentIndex].BName[i] = 0
	}
	copy(folderBlock.BContent[targetContentIndex].BName[:], name)
	file.Seek(int64(sb.SBlockStart+targetBlockIndex*sb.SBlockSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
		return fmt.Sprintf("Error al actualizar bloque %d: %v", targetBlockIndex, err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("%s renombrado a %s exitosamente", fileName, name)
}

// UNMOUNT: Desmonta una partición.
func unmount(params map[string]string) string {
	id, hasID := params["id"]
	if !hasID {
		return "Error: Parámetro -id es obligatorio"
	}

	// Verificar si la partición está montada
	var mpIndex = -1
	for i, mp := range mountedPartitions {
		if mp.ID == id {
			mpIndex = i
			break
		}
	}
	if mpIndex == -1 {
		return fmt.Sprintf("Error: Partición %s no está montada", id)
	}

	// Verificar si hay sesión activa en esta partición
	if currentSession != nil && currentSession.PartID == id {
		return fmt.Sprintf("Error: No se puede desmontar %s, hay una sesión activa", id)
	}

	// Actualizar MBR o EBR
	file, err := os.OpenFile(mountedPartitions[mpIndex].Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}

	partitionIndex := -1
	for i, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' && strings.Trim(string(part.PartName[:]), "\x00") == mountedPartitions[mpIndex].Name {
			partitionIndex = i
			break
		}
	}

	if partitionIndex != -1 {
		mbr.MbrPartitions[partitionIndex].PartCorrel = -1
		mbr.MbrPartitions[partitionIndex].PartID = [4]byte{}
		if err = writeMBR(file, mbr); err != nil {
			return fmt.Sprintf("Error al escribir MBR: %v", err)
		}
	} else {
		for _, part := range mbr.MbrPartitions {
			if part.PartStatus == '1' && part.PartType == 'E' {
				currentPos := part.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err = binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						break
					}
					if strings.Trim(string(ebr.PartName[:]), "\x00") == mountedPartitions[mpIndex].Name {
						ebr.PartMount = '0'
						ebr.PartCorrel = -1
						ebr.PartID = [4]byte{}
						file.Seek(int64(currentPos), 0)
						if err = binary.Write(file, binary.LittleEndian, &ebr); err != nil {
							return fmt.Sprintf("Error al escribir EBR: %v", err)
						}
						break
					}
					currentPos = ebr.PartNext
				}
			}
		}
	}

	// Remover de la lista de particiones montadas
	mountedPartitions = append(mountedPartitions[:mpIndex], mountedPartitions[mpIndex+1:]...)

	return fmt.Sprintf("Partición %s desmontada exitosamente", id)
}

// RECOVERY: No implementado (requiere journaling EXT3).
func recovery(params map[string]string) string {
	return "Error: Comando RECOVERY no implementado para EXT2"
}

// LOSS: Simula pérdida de datos en una partición.
func loss(params map[string]string) string {
	id, hasID := params["id"]
	if !hasID {
		return "Error: Parámetro -id es obligatorio"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede ejecutar LOSS"
	}

	// Verificar partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == id {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", id)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Resetear bitmaps
	bitmapInodes := make([]byte, sb.SInodesCount)
	bitmapBlocks := make([]byte, sb.SBlocksCount)
	bitmapInodes[0], bitmapInodes[1] = 1, 1
	bitmapBlocks[0], bitmapBlocks[1] = 1, 1
	file.Seek(int64(sb.SBmInodeStart), 0)
	if _, err = file.Write(bitmapInodes); err != nil {
		return fmt.Sprintf("Error al escribir bitmap de inodos: %v", err)
	}
	file.Seek(int64(sb.SBmBlockStart), 0)
	if _, err = file.Write(bitmapBlocks); err != nil {
		return fmt.Sprintf("Error al escribir bitmap de bloques: %v", err)
	}

	// Actualizar superbloque
	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}
	var partStart int32
	foundPart := false
	for _, p := range mbr.MbrPartitions {
		name := strings.Trim(string(p.PartName[:]), "\x00")
		if name == mp.Name {
			partStart = p.PartStart
			foundPart = true
			break
		}
	}
	if !foundPart {
		for _, p := range mbr.MbrPartitions {
			if p.PartType == 'E' {
				currentPos := p.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err = binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						continue
					}
					name := strings.Trim(string(ebr.PartName[:]), "\x00")
					if name == mp.Name && ebr.PartSize > 0 {
						partStart = ebr.PartStart
						foundPart = true
						break
					}
					currentPos = ebr.PartNext
				}
			}
			if foundPart {
				break
			}
		}
	}
	if !foundPart {
		return fmt.Sprintf("Error: Partición %s no encontrada en MBR ni EBR", mp.Name)
	}

	file.Seek(int64(partStart), 0)
	var sbUpdated Superblock
	if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}
	sbUpdated.SFreeInodesCount = sb.SInodesCount - 2
	sbUpdated.SFreeBlocksCount = sb.SBlocksCount - 2
	file.Seek(int64(partStart), 0)
	if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("Pérdida de datos simulada en partición %s", id)
}
