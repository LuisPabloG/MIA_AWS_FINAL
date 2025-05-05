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

func normalizePath(path string) ([]string, error) {
	path = strings.Trim(path, "\"")
	parts := strings.Split(path, "/")
	var result []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) > 12 {
			return nil, fmt.Errorf("el nombre %s excede 12 caracteres", part)
		}
		result = append(result, part)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("ruta inválida")
	}
	return result, nil
}

// MKFILE: Crea un archivo en la ruta especificada con contenido o tamaño dado.
func mkfile(params map[string]string) string {
	var err error
	path, hasPath := params["path"]
	if !hasPath {
		return "Error: Parámetro -path es obligatorio"
	}

	if len(path) > 255 {
		return "Error: La ruta excede el límite de caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Obtener parámetros opcionales
	sizeStr, hasSize := params["size"]
	cont, hasCont := params["cont"]
	size := int32(0)
	if hasSize {
		s, err := strconv.Atoi(sizeStr)
		if err != nil || s < 0 {
			return "Error: Tamaño inválido"
		}
		size = int32(s)
	}
	if !hasCont && !hasSize {
		return "Error: Se requiere -cont o -size"
	}
	if hasCont && hasSize {
		return "Error: No se pueden especificar -cont y -size juntos"
	}

	// Obtener partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == currentSession.PartID {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", currentSession.PartID)
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

	// Calcular partStart como en readSuperblock
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
					if err := binary.Read(file, binary.LittleEndian, &ebr); err != nil {
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

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	fileName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]

	// Navegar hasta la carpeta padre
	currentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Verificar permisos de escritura en la carpeta padre
	parentInode, err := readInode(file, sb, currentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo padre %d: %v", currentInode, err)
	}
	if !hasWritePermission(parentInode, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para escribir en la carpeta padre"
	}

	// Verificar si el archivo ya existe
	for _, blockIndex := range parentInode.IBlock {
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
				return fmt.Sprintf("Error: El archivo %s ya existe", fileName)
			}
		}
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

	// Encontrar inodo libre
	newInodeIndex := int32(-1)
	for i := int32(0); i < sb.SInodesCount; i++ {
		if bitmapInodes[i] == 0 {
			newInodeIndex = i
			bitmapInodes[i] = 1
			break
		}
	}
	if newInodeIndex == -1 {
		return "Error: No hay inodos libres"
	}

	// Preparar contenido
	var content []byte
	if hasCont {
		content = []byte(cont)
	} else {
		content = make([]byte, size)
		for i := range content {
			content[i] = '0' // Rellenar con '0'
		}
	}

	// Calcular bloques necesarios
	numBlocks := int32(math.Ceil(float64(len(content)) / 64.0))
	if numBlocks > 15 {
		bitmapInodes[newInodeIndex] = 0
		file.Seek(int64(sb.SBmInodeStart), 0)
		file.Write(bitmapInodes)
		return fmt.Sprintf("Error: El archivo requiere %d bloques, máximo 15", numBlocks)
	}

	// Asignar bloques
	newBlocks := make([]int32, numBlocks)
	currentBlock := int32(2) // Comenzar después de bloques iniciales
	for i := int32(0); i < numBlocks; i++ {
		for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
			currentBlock++
		}
		if currentBlock >= sb.SBlocksCount {
			bitmapInodes[newInodeIndex] = 0
			file.Seek(int64(sb.SBmInodeStart), 0)
			file.Write(bitmapInodes)
			return "Error: No hay bloques libres"
		}
		newBlocks[i] = currentBlock
		bitmapBlocks[currentBlock] = 1
		currentBlock++
	}

	// Crear inodo para el archivo
	fecha := time.Now().Format("2006-01-02 15:04:05")
	newInode := Inode{
		IUid:  currentSession.UserID,
		IGid:  currentSession.GroupID,
		ISize: int32(len(content)),
		IType: '1',
		IPerm: 664,
	}
	for i, blockIndex := range newBlocks {
		newInode.IBlock[i] = blockIndex
	}
	copy(newInode.IAtime[:], fecha)
	copy(newInode.ICtime[:], fecha)
	copy(newInode.IMtime[:], fecha)

	// Escribir bloques de archivo
	for i, blockIndex := range newBlocks {
		var block FileBlock
		for j := range block.BContent {
			block.BContent[j] = 0
		}
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

	// Escribir inodo
	file.Seek(int64(sb.SInodeStart+newInodeIndex*sb.SInodeSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &newInode); err != nil {
		return fmt.Sprintf("Error al escribir inodo %d: %v", newInodeIndex, err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	// Actualizar carpeta padre
	found := false
	for _, blockIndex := range parentInode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for i := range folderBlock.BContent {
			name := strings.Trim(string(folderBlock.BContent[i].BName[:]), "\x00")
			if name == "" {
				copy(folderBlock.BContent[i].BName[:], fileName)
				folderBlock.BContent[i].BInode = newInodeIndex
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
		// Asignar nuevo bloque a la carpeta padre si no hay espacio
		for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
			currentBlock++
		}
		if currentBlock >= sb.SBlocksCount {
			// Liberar inodo y bloques asignados
			bitmapInodes[newInodeIndex] = 0
			for _, blockIndex := range newBlocks {
				bitmapBlocks[blockIndex] = 0
			}
			file.Seek(int64(sb.SBmInodeStart), 0)
			file.Write(bitmapInodes)
			file.Seek(int64(sb.SBmBlockStart), 0)
			file.Write(bitmapBlocks)
			return "Error: No hay bloques libres para la carpeta padre"
		}
		newFolderBlockIndex := currentBlock
		bitmapBlocks[currentBlock] = 1

		var newFolderBlock FolderBlock
		copy(newFolderBlock.BContent[0].BName[:], fileName)
		newFolderBlock.BContent[0].BInode = newInodeIndex
		file.Seek(int64(sb.SBlockStart+newFolderBlockIndex*sb.SBlockSize), 0)
		if err = binary.Write(file, binary.LittleEndian, &newFolderBlock); err != nil {
			return fmt.Sprintf("Error al escribir bloque %d: %v", newFolderBlockIndex, err)
		}

		// Actualizar inodo padre
		for i := range parentInode.IBlock {
			if parentInode.IBlock[i] == -1 {
				parentInode.IBlock[i] = newFolderBlockIndex
				break
			}
		}
		file.Seek(int64(sb.SInodeStart+currentInode*sb.SInodeSize), 0)
		if err = binary.Write(file, binary.LittleEndian, &parentInode); err != nil {
			return fmt.Sprintf("Error al actualizar inodo padre %d: %v", currentInode, err)
		}
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
	file.Seek(int64(partStart), 0)
	var sbUpdated Superblock
	if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}
	sbUpdated.SFreeInodesCount--
	sbUpdated.SFreeBlocksCount -= numBlocks
	if !found {
		sbUpdated.SFreeBlocksCount-- // Bloque adicional para la carpeta padre
	}
	file.Seek(int64(partStart), 0)
	if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	return fmt.Sprintf("Archivo %s creado exitosamente", path)
}

// MKDIR: Crea una carpeta en la ruta especificada, con soporte para creación recursiva (-p).
// MKDIR: Crea una carpeta en la ruta especificada, con soporte para creación recursiva (-p).
func mkdir(params map[string]string) string {
	var err error
	path, hasPath := params["path"]
	if !hasPath {
		return "Error: Parámetro -path es obligatorio"
	}

	if len(path) > 255 {
		return "Error: La ruta excede el límite de caracteres"
	}

	createParents := false
	if _, hasP := params["p"]; hasP {
		createParents = true
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}

	// Obtener partición montada
	var mp *MountedPartition
	for _, p := range mountedPartitions {
		if p.ID == currentSession.PartID {
			mp = &p
			break
		}
	}
	if mp == nil {
		return fmt.Sprintf("Error: Partición %s no encontrada", currentSession.PartID)
	}

	file, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	// Validar que la partición existe en el MBR o EBR
	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}
	foundPart := false
	for _, p := range mbr.MbrPartitions {
		name := strings.Trim(string(p.PartName[:]), "\x00")
		if name == mp.Name {
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

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(path)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if len(pathParts) == 0 {
		return "Error: La ruta no puede ser vacía"
	}

	if createParents {
		currentInode := int32(0) // Comenzar desde la raíz
		currentPathParts := []string{}
		for _, part := range pathParts {
			currentPathParts = append(currentPathParts, part)
			// Intentar navegar a la carpeta actual
			nextInode, err := navigateToParent(file, sb, currentPathParts)
			if err != nil {
				// La carpeta no existe, crearla
				result := createFolder(file, sb, mp, currentInode, part)
				if strings.HasPrefix(result, "Error") {
					return result
				}
				// Actualizar currentInode
				nextInode, err = navigateToParent(file, sb, currentPathParts)
				if err != nil {
					return fmt.Sprintf("Error al navegar a %s después de crearla: %v", part, err)
				}
			}
			currentInode = nextInode
		}
		return fmt.Sprintf("Carpeta %s creada exitosamente", path)
	}

	// Creación no recursiva
	folderName := pathParts[len(pathParts)-1]
	parentPath := pathParts[:len(pathParts)-1]
	currentInode, err := navigateToParent(file, sb, parentPath)
	if err != nil {
		return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
	}

	// Crear la carpeta
	return createFolder(file, sb, mp, currentInode, folderName)
}

// createFolder: Función auxiliar para crear una carpeta en el sistema de archivos.
func createFolder(file *os.File, sb Superblock, mp *MountedPartition, parentInodeIndex int32, folderName string) string {
	var err error
	fmt.Printf("Creating folder %s, parentInode=%d\n", folderName, parentInodeIndex)

	// Calcular partStart para el superbloque
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

	// Verificar permisos de escritura en la carpeta padre
	parentInode, err := readInode(file, sb, parentInodeIndex)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo padre %d: %v", parentInodeIndex, err)
	}
	if !hasWritePermission(parentInode, currentSession.UserID, currentSession.GroupID) {
		return "Error: Permisos insuficientes para escribir en la carpeta padre"
	}

	// Verificar si la carpeta ya existe
	for _, blockIndex := range parentInode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == folderName {
				return fmt.Sprintf("Error: La carpeta %s ya existe", folderName)
			}
		}
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

	// Encontrar inodo libre
	newInodeIndex := int32(-1)
	for i := int32(0); i < sb.SInodesCount; i++ {
		if bitmapInodes[i] == 0 {
			newInodeIndex = i
			bitmapInodes[i] = 1
			break
		}
	}
	if newInodeIndex == -1 {
		return "Error: No hay inodos libres"
	}
	fmt.Printf("Allocated inode=%d\n", newInodeIndex)

	// Encontrar bloque libre
	currentBlock := int32(2)
	for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
		currentBlock++
	}
	if currentBlock >= sb.SBlocksCount {
		bitmapInodes[newInodeIndex] = 0
		file.Seek(int64(sb.SBmInodeStart), 0)
		file.Write(bitmapInodes)
		return "Error: No hay bloques libres"
	}
	newBlockIndex := currentBlock
	bitmapBlocks[currentBlock] = 1
	fmt.Printf("Allocated block=%d\n", newBlockIndex)

	// Crear inodo para la carpeta
	fecha := time.Now().Format("2006-01-02 15:04:05")
	newInode := Inode{
		IUid:  currentSession.UserID,
		IGid:  currentSession.GroupID,
		ISize: 0,
		IType: '0',
		IPerm: 664,
	}
	newInode.IBlock[0] = newBlockIndex
	copy(newInode.IAtime[:], fecha)
	copy(newInode.ICtime[:], fecha)
	copy(newInode.IMtime[:], fecha)

	// Crear bloque de carpeta con entradas . y ..
	var folderBlock FolderBlock
	// Inicializar todas las entradas como vacías
	for i := range folderBlock.BContent {
		folderBlock.BContent[i].BInode = -1
	}
	// Entrada para .
	copy(folderBlock.BContent[0].BName[:], ".")
	folderBlock.BContent[0].BInode = newInodeIndex
	// Entrada para ..
	copy(folderBlock.BContent[1].BName[:], "..")
	folderBlock.BContent[1].BInode = parentInodeIndex

	// Escribir inodo
	file.Seek(int64(sb.SInodeStart+newInodeIndex*sb.SInodeSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &newInode); err != nil {
		return fmt.Sprintf("Error al escribir inodo %d: %v", newInodeIndex, err)
	}
	fmt.Printf("Wrote inode=%d\n", newInodeIndex)

	// Escribir bloque
	file.Seek(int64(sb.SBlockStart+newBlockIndex*sb.SBlockSize), 0)
	if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
		return fmt.Sprintf("Error al escribir bloque %d: %v", newBlockIndex, err)
	}
	fmt.Printf("Wrote block=%d\n", newBlockIndex)

	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	// Actualizar carpeta padre
	found := false
	for i, blockIndex := range parentInode.IBlock {
		if blockIndex == -1 {
			// Crear un nuevo bloque si no hay espacio
			currentBlock++
			for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
				currentBlock++
			}
			if currentBlock >= sb.SBlocksCount {
				// Liberar inodo y bloque asignados
				bitmapInodes[newInodeIndex] = 0
				bitmapBlocks[newBlockIndex] = 0
				file.Seek(int64(sb.SBmInodeStart), 0)
				file.Write(bitmapInodes)
				file.Seek(int64(sb.SBmBlockStart), 0)
				file.Write(bitmapBlocks)
				return "Error: No hay bloques libres para la carpeta padre"
			}
			newFolderBlockIndex := currentBlock
			bitmapBlocks[currentBlock] = 1
			fmt.Printf("Allocated new folder block=%d for parent\n", newFolderBlockIndex)

			var newFolderBlock FolderBlock
			// Inicializar todas las entradas como vacías
			for j := range newFolderBlock.BContent {
				newFolderBlock.BContent[j].BInode = -1
			}
			// Añadir la entrada para el nuevo directorio
			copy(newFolderBlock.BContent[0].BName[:], folderName)
			newFolderBlock.BContent[0].BInode = newInodeIndex
			file.Seek(int64(sb.SBlockStart+newFolderBlockIndex*sb.SBlockSize), 0)
			if err = binary.Write(file, binary.LittleEndian, &newFolderBlock); err != nil {
				return fmt.Sprintf("Error al escribir bloque %d: %v", newFolderBlockIndex, err)
			}

			// Actualizar inodo padre
			parentInode.IBlock[i] = newFolderBlockIndex
			file.Seek(int64(sb.SInodeStart+parentInodeIndex*sb.SInodeSize), 0)
			if err = binary.Write(file, binary.LittleEndian, &parentInode); err != nil {
				return fmt.Sprintf("Error al actualizar inodo padre %d: %v", parentInodeIndex, err)
			}
			fmt.Printf("Updated parent inode=%d with new block=%d\n", parentInodeIndex, newFolderBlockIndex)
			found = true
			break
		}

		folderBlock, err = readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for j := range folderBlock.BContent {
			name := strings.Trim(string(folderBlock.BContent[j].BName[:]), "\x00")
			if name == "" || folderBlock.BContent[j].BInode == -1 {
				copy(folderBlock.BContent[j].BName[:], folderName)
				folderBlock.BContent[j].BInode = newInodeIndex
				file.Seek(int64(sb.SBlockStart+blockIndex*sb.SBlockSize), 0)
				if err = binary.Write(file, binary.LittleEndian, &folderBlock); err != nil {
					return fmt.Sprintf("Error al actualizar bloque %d: %v", blockIndex, err)
				}
				fmt.Printf("Updated parent block=%d with folder %s, inode=%d\n", blockIndex, folderName, newInodeIndex)
				found = true
				break
			}
		}
		if found {
			break
		}
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
	file.Seek(int64(partStart), 0)
	var sbUpdated Superblock
	if err = binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}
	sbUpdated.SFreeInodesCount--
	sbUpdated.SFreeBlocksCount--
	if !found {
		sbUpdated.SFreeBlocksCount-- // Bloque adicional para la carpeta padre
	}
	file.Seek(int64(partStart), 0)
	if err = binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Sprintf("Error syncing disk: %v", err)
	}

	fmt.Printf("Folder %s created successfully\n", folderName)
	return fmt.Sprintf("Carpeta %s creada exitosamente", folderName)
}

// navigateToParent: Navega hasta la carpeta padre de una ruta.
func navigateToParent(file *os.File, sb Superblock, pathParts []string) (int32, error) {
	currentInode := int32(0) // Inodo raíz
	for _, part := range pathParts {
		if len(part) > 12 {
			return 0, fmt.Errorf("el nombre %s excede 12 caracteres", part)
		}
		inode, err := readInode(file, sb, currentInode)
		if err != nil {
			return 0, fmt.Errorf("error al leer inodo %d: %v", currentInode, err)
		}
		if inode.IType != '0' {
			return 0, fmt.Errorf("%s no es una carpeta", part)
		}

		found := false
		for _, blockIndex := range inode.IBlock {
			if blockIndex == -1 {
				continue
			}
			folderBlock, err := readFolderBlock(file, sb, blockIndex)
			if err != nil {
				return 0, fmt.Errorf("error al leer bloque %d: %v", blockIndex, err)
			}
			for _, content := range folderBlock.BContent {
				name := strings.Trim(string(content.BName[:]), "\x00")
				if name == part {
					currentInode = content.BInode
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return 0, fmt.Errorf("la carpeta %s no existe", part)
		}
	}
	return currentInode, nil
}

// hasWritePermission: Verifica permisos de escritura en un inodo.
func hasWritePermission(inode Inode, uid, gid int32) bool {
	if currentSession.Username == "root" {
		return true
	}

	ownerPerm := (inode.IPerm / 100) % 10
	groupPerm := (inode.IPerm / 10) % 10
	otherPerm := inode.IPerm % 10

	if inode.IUid == uid {
		return ownerPerm >= 2
	}
	if inode.IGid == gid {
		return groupPerm >= 2
	}
	return otherPerm >= 2
}
