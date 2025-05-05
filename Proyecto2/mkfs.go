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

// Superblock: Actualizado para EXT3 con journaling
type Superblock struct {
	SFilesystemType  int32
	SInodesCount     int32
	SBlocksCount     int32
	SFreeBlocksCount int32
	SFreeInodesCount int32
	SMtime           [19]byte
	SUmtime          [19]byte
	SMntCount        int32
	SMagic           int32
	SInodeSize       int32
	SBlockSize       int32
	SFirstIno        int32
	SFirstBlo        int32
	SBmInodeStart    int32
	SBmBlockStart    int32
	SInodeStart      int32
	SBlockStart      int32
	SJournalStart    int32 // Inicio del journal
	SJournalSize     int32 // Tamaño del journal
}

type Inode struct {
	IUid   int32
	IGid   int32
	ISize  int32
	IAtime [19]byte
	ICtime [19]byte
	IMtime [19]byte
	IBlock [15]int32
	IType  byte
	IPerm  int32
}

type FolderBlock struct {
	BContent [4]struct {
		BName  [12]byte
		BInode int32
	}
}

type FileBlock struct {
	BContent [64]byte
}

// Estructura para la sesión activa
type Session struct {
	UserID   int32
	Username string
	GroupID  int32
	PartID   string
}

// Variable global para la sesión activa
var currentSession *Session

// readSuperblock lee el superbloque de una partición
func readSuperblock(file *os.File, mp *MountedPartition) (Superblock, error) {
	var sb Superblock

	// Leer el MBR para obtener la posición inicial de la partición
	mbr, err := readMBR(file)
	if err != nil {
		return Superblock{}, fmt.Errorf("error al leer MBR: %v", err)
	}

	// Buscar la partición correspondiente en el MBR
	var partStart int32
	var partSize int32
	found := false
	for _, p := range mbr.MbrPartitions {
		name := strings.Trim(string(p.PartName[:]), "\x00")
		fmt.Printf("MBR PartName: %s, Expected: %s\n", name, mp.Name)
		if name == mp.Name {
			partStart = p.PartStart
			partSize = p.PartSize
			found = true
			break
		}
	}

	// Si no se encontró en el MBR, buscar en EBR para particiones lógicas
	if !found {
		for _, p := range mbr.MbrPartitions {
			if p.PartType == 'E' {
				currentPos := p.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err := binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						fmt.Printf("Error leyendo EBR en pos %d: %v\n", currentPos, err)
						break
					}
					name := strings.Trim(string(ebr.PartName[:]), "\x00")
					fmt.Printf("EBR PartName: %s, Expected: %s, Start: %d, Next: %d\n",
						name, mp.Name, ebr.PartStart, ebr.PartNext)
					if name == mp.Name && ebr.PartSize > 0 {
						partStart = ebr.PartStart
						partSize = ebr.PartSize
						found = true
						break
					}
					currentPos = ebr.PartNext
				}
			}
			if found {
				break
			}
		}
	}

	if !found {
		return Superblock{}, fmt.Errorf("partición %s no encontrada en MBR ni EBR", mp.Name)
	}

	// Validar tamaño de partición
	if partSize <= 0 {
		return Superblock{}, fmt.Errorf("tamaño de partición %s es inválido: %d", mp.Name, partSize)
	}

	// Validar partStart
	if partStart <= 0 {
		return Superblock{}, fmt.Errorf("posición inicial de partición %s inválida: %d", mp.Name, partStart)
	}

	// Leer el superbloque desde la posición inicial de la partición
	file.Seek(int64(partStart), 0)
	if err := binary.Read(file, binary.LittleEndian, &sb); err != nil {
		return Superblock{}, fmt.Errorf("error al leer superbloque: %v", err)
	}

	// Validar SMagic para confirmar que es EXT2
	if sb.SMagic != 0xEF53 {
		return Superblock{}, fmt.Errorf("superbloque inválido para %s, SMagic=%x", mp.Name, sb.SMagic)
	}

	return sb, nil
}

// MKFS: Formatea una partición con EXT2
func mkfs(params map[string]string) string {
	id, hasID := params["id"]
	if !hasID {
		return "Error: Parámetro -id es obligatorio"
	}

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

	mbr, err := readMBR(file)
	if err != nil {
		return fmt.Sprintf("Error al leer MBR: %v", err)
	}

	var part Partition
	found := false
	for _, p := range mbr.MbrPartitions {
		if strings.Trim(string(p.PartName[:]), "\x00") == mp.Name {
			part = p
			found = true
			break
		}
	}

	// Buscar en EBR si no se encuentra en MBR
	if !found {
		for _, p := range mbr.MbrPartitions {
			if p.PartType == 'E' {
				currentPos := p.PartStart
				for currentPos != -1 {
					file.Seek(int64(currentPos), 0)
					var ebr EBR
					if err := binary.Read(file, binary.LittleEndian, &ebr); err != nil {
						continue
					}
					if strings.Trim(string(ebr.PartName[:]), "\x00") == mp.Name && ebr.PartSize > 0 {
						part = Partition{
							PartStart: ebr.PartStart,
							PartSize:  ebr.PartSize,
							PartName:  ebr.PartName,
							PartType:  'L',
						}
						found = true
						break
					}
					currentPos = ebr.PartNext
				}
			}
			if found {
				break
			}
		}
	}

	if !found {
		return fmt.Sprintf("Error: Partición %s no encontrada en MBR ni EBR", mp.Name)
	}

	partSize := part.PartSize
	superblockSize := int32(binary.Size(Superblock{}))
	inodeSize := int32(binary.Size(Inode{}))
	blockSize := int32(64)
	if partSize <= superblockSize {
		return fmt.Sprintf("Error: Tamaño de partición %d es demasiado pequeño para superbloque %d", partSize, superblockSize)
	}

	n := float64(partSize-superblockSize) / float64(1+3+inodeSize+3*blockSize)
	numStructs := int32(math.Floor(n))
	if numStructs <= 0 {
		return fmt.Sprintf("Error: No hay espacio suficiente para estructuras EXT2 (numStructs=%d)", numStructs)
	}

	// Inicializar superbloque
	fecha := time.Now().Format("2006-01-02 15:04:05")
	sb := Superblock{
		SFilesystemType:  2,
		SInodesCount:     numStructs,
		SBlocksCount:     3 * numStructs,
		SFreeInodesCount: numStructs - 2,
		SFreeBlocksCount: 3*numStructs - 2,
		SMagic:           0xEF53,
		SInodeSize:       inodeSize,
		SBlockSize:       blockSize,
		SBmInodeStart:    part.PartStart + superblockSize,
		SBmBlockStart:    part.PartStart + superblockSize + numStructs,
		SInodeStart:      part.PartStart + superblockSize + numStructs + 3*numStructs,
		SBlockStart:      part.PartStart + superblockSize + numStructs + 3*numStructs + numStructs*inodeSize,
		SFirstIno:        2,
		SFirstBlo:        2,
	}
	copy(sb.SMtime[:], fecha)
	copy(sb.SUmtime[:], fecha)
	sb.SMntCount = 1

	// Escribir superbloque
	file.Seek(int64(part.PartStart), 0)
	if err := binary.Write(file, binary.LittleEndian, &sb); err != nil {
		return fmt.Sprintf("Error al escribir superbloque: %v", err)
	}

	// Inicializar bitmaps
	bitmapInodes := make([]byte, numStructs)
	bitmapBlocks := make([]byte, 3*numStructs)
	bitmapInodes[0], bitmapInodes[1] = 1, 1
	bitmapBlocks[0], bitmapBlocks[1] = 1, 1
	file.Seek(int64(sb.SBmInodeStart), 0)
	file.Write(bitmapInodes)
	file.Seek(int64(sb.SBmBlockStart), 0)
	file.Write(bitmapBlocks)

	// Crear inodo raíz
	inodeRoot := Inode{
		IUid:  1,
		IGid:  1,
		ISize: 0,
		IType: '0',
		IPerm: 777,
	}
	inodeRoot.IBlock[0] = 0
	copy(inodeRoot.IAtime[:], fecha)
	copy(inodeRoot.ICtime[:], fecha)
	copy(inodeRoot.IMtime[:], fecha)

	folderBlock := FolderBlock{}
	copy(folderBlock.BContent[0].BName[:], ".")
	folderBlock.BContent[0].BInode = 0
	copy(folderBlock.BContent[1].BName[:], "..")
	folderBlock.BContent[1].BInode = 0
	copy(folderBlock.BContent[2].BName[:], "users.txt")
	folderBlock.BContent[2].BInode = 1

	// Crear inodo para users.txt
	usersContent := "1,G,root\n1,U,root,root,123\n"
	inodeUsers := Inode{
		IUid:  1,
		IGid:  1,
		ISize: int32(len(usersContent)),
		IType: '1',
		IPerm: 777,
	}
	inodeUsers.IBlock[0] = 1
	copy(inodeUsers.IAtime[:], fecha)
	copy(inodeUsers.ICtime[:], fecha)
	copy(inodeUsers.IMtime[:], fecha)

	fileBlock := FileBlock{}
	copy(fileBlock.BContent[:], usersContent)

	// Escribir inodos
	file.Seek(int64(sb.SInodeStart), 0)
	binary.Write(file, binary.LittleEndian, &inodeRoot)
	binary.Write(file, binary.LittleEndian, &inodeUsers)

	// Escribir bloques
	file.Seek(int64(sb.SBlockStart), 0)
	binary.Write(file, binary.LittleEndian, &folderBlock)
	binary.Write(file, binary.LittleEndian, &fileBlock)

	return fmt.Sprintf("Partición %s formateada exitosamente", id)
}

// CAT: Muestra el contenido de un archivo
func cat(params map[string]string) string {
	file, hasFile := params["file"]
	id, hasID := params["id"]
	if !hasFile || !hasID {
		return "Error: Parámetros -file y -id son obligatorios"
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

	f, err := os.OpenFile(mp.Path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer f.Close()

	sb, err := readSuperblock(f, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	// Procesar la ruta
	pathParts, err := normalizePath(file)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	// Navegar hasta la carpeta padre
	parentPath := pathParts[:len(pathParts)-1]
	fileName := pathParts[len(pathParts)-1]
	currentInode := int32(0) // Raíz por defecto
	if len(parentPath) > 0 {
		currentInode, err = navigateToParent(f, sb, parentPath)
		if err != nil {
			return fmt.Sprintf("Error al navegar a la carpeta padre: %v", err)
		}
	}

	// Buscar el archivo
	inode, err := readInode(f, sb, currentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo: %v", err)
	}
	if inode.IType != '0' {
		return fmt.Sprintf("Error: %s no es una carpeta", file)
	}

	var fileInode int32 = -1
	for _, blockIndex := range inode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(f, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque: %v", err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == fileName {
				fileInode = content.BInode
				break
			}
		}
		if fileInode != -1 {
			break
		}
	}
	if fileInode == -1 {
		return fmt.Sprintf("Error: Archivo %s no encontrado", fileName)
	}

	// Leer el inodo del archivo
	fileInodeData, err := readInode(f, sb, fileInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo del archivo: %v", err)
	}
	if fileInodeData.IType != '1' {
		return fmt.Sprintf("Error: %s no es un archivo", fileName)
	}

	// Verificar permisos de lectura
	if !hasReadPermission(fileInodeData, currentSession) {
		return fmt.Sprintf("Error: Permiso denegado para leer %s", fileName)
	}

	// Leer contenido
	var content strings.Builder
	for _, blockIndex := range fileInodeData.IBlock {
		if blockIndex == -1 {
			continue
		}
		fileBlock, err := readFileBlock(f, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque de archivo: %v", err)
		}
		content.Write(fileBlock.BContent[:])
	}

	return strings.Trim(content.String(), "\x00")
}

func hasReadPermission(inode Inode, session *Session) bool {
	if session == nil {
		return false
	}
	if session.Username == "root" {
		return true
	}
	perm := inode.IPerm
	if inode.IUid == session.UserID {
		return perm/100 >= 4 // Permiso de lectura para propietario
	}
	if inode.IGid == session.GroupID {
		return (perm%100)/10 >= 4 // Permiso de lectura para grupo
	}
	return perm%10 >= 4 // Permiso de lectura para otros
}

// LOGIN: Inicia una sesión de usuario
func login(params map[string]string) string {
	user, hasUser := params["user"]
	pass, hasPass := params["pass"]
	id, hasID := params["id"]
	if !hasUser || !hasPass || !hasID {
		return "Error: Parámetros -user, -pass y -id son obligatorios"
	}

	if len(user) > 10 || len(pass) > 10 {
		return "Error: Usuario y contraseña no deben exceder 10 caracteres"
	}

	if currentSession != nil {
		return "Error: Ya existe una sesión activa"
	}

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

	file, err := os.Open(mp.Path)
	if err != nil {
		return fmt.Sprintf("Error al abrir disco: %v", err)
	}
	defer file.Close()

	sb, err := readSuperblock(file, mp)
	if err != nil {
		return fmt.Sprintf("Error al leer superbloque: %v", err)
	}

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 5 || parts[1] != "U" {
			continue
		}
		if strings.TrimSpace(parts[3]) == user && strings.TrimSpace(parts[4]) == pass {
			uid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			currentSession = &Session{
				UserID:   int32(uid),
				Username: user,
				GroupID:  1,
				PartID:   id,
			}
			return fmt.Sprintf("Sesión iniciada para %s", user)
		}
	}

	return "Error: Credenciales incorrectas"
}

// LOGOUT: Cierra la sesión activa
func logout(params map[string]string) string {
	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	username := currentSession.Username
	currentSession = nil
	return fmt.Sprintf("Sesión cerrada para %s", username)
}

// MKGRP: Crea un grupo
func mkgrp(params map[string]string) string {
	name, hasName := params["name"]
	if !hasName {
		return "Error: Parámetro -name es obligatorio"
	}

	if len(name) > 10 {
		return "Error: El nombre del grupo no debe exceder 10 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede crear grupos"
	}

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

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	maxGID := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 || parts[1] != "G" {
			continue
		}
		if strings.TrimSpace(parts[2]) == name {
			return fmt.Sprintf("Error: El grupo %s ya existe", name)
		}
		gid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		if gid > maxGID {
			maxGID = gid
		}
	}

	newLine := fmt.Sprintf("%d,G,%s\n", maxGID+1, name)
	usersContent += newLine
	if err := writeUsersTxt(file, sb, usersContent); err != nil {
		return fmt.Sprintf("Error al escribir users.txt: %v", err)
	}

	return fmt.Sprintf("Grupo %s creado exitosamente", name)
}

// RMGRP: Elimina un grupo
func rmgrp(params map[string]string) string {
	name, hasName := params["name"]
	if !hasName {
		return "Error: Parámetro -name es obligatorio"
	}

	if len(name) > 10 {
		return "Error: El nombre del grupo no debe exceder 10 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede eliminar grupos"
	}

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

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	var newContent strings.Builder
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 && parts[1] == "G" && strings.TrimSpace(parts[2]) == name {
			found = true
			continue
		}
		newContent.WriteString(line + "\n")
	}

	if !found {
		return fmt.Sprintf("Error: El grupo %s no existe", name)
	}

	if err := writeUsersTxt(file, sb, newContent.String()); err != nil {
		return fmt.Sprintf("Error al escribir users.txt: %v", err)
	}

	return fmt.Sprintf("Grupo %s eliminado exitosamente", name)
}

// MKUSR: Crea un usuario
func mkusr(params map[string]string) string {
	user, hasUser := params["user"]
	pass, hasPass := params["pass"]
	grp, hasGrp := params["grp"]
	if !hasUser || !hasPass || !hasGrp {
		return "Error: Parámetros -user, -pass y -grp son obligatorios"
	}

	if len(user) > 10 || len(pass) > 10 || len(grp) > 10 {
		return "Error: Usuario, contraseña y grupo no deben exceder 10 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede crear usuarios"
	}

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

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	maxUID := 0
	groupExists := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		if parts[1] == "G" && strings.TrimSpace(parts[2]) == grp {
			groupExists = true
		}
		if parts[1] == "U" && strings.TrimSpace(parts[3]) == user {
			return fmt.Sprintf("Error: El usuario %s ya existe", user)
		}
		if parts[1] == "U" {
			uid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			if uid > maxUID {
				maxUID = uid
			}
		}
	}

	if !groupExists {
		return fmt.Sprintf("Error: El grupo %s no existe", grp)
	}

	newLine := fmt.Sprintf("%d,U,%s,%s,%s\n", maxUID+1, grp, user, pass)
	usersContent += newLine
	if err := writeUsersTxt(file, sb, usersContent); err != nil {
		return fmt.Sprintf("Error al escribir users.txt: %v", err)
	}

	return fmt.Sprintf("Usuario %s creado exitosamente", user)
}

// RMUSR: Elimina un usuario
func rmusr(params map[string]string) string {
	user, hasUser := params["user"]
	if !hasUser {
		return "Error: Parámetro -user es obligatorio"
	}

	if len(user) > 10 {
		return "Error: El nombre del usuario no debe exceder 10 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede eliminar usuarios"
	}

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

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	var newContent strings.Builder
	found := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 4 && parts[1] == "U" && strings.TrimSpace(parts[3]) == user {
			found = true
			continue
		}
		newContent.WriteString(line + "\n")
	}

	if !found {
		return fmt.Sprintf("Error: El usuario %s no existe", user)
	}

	if err := writeUsersTxt(file, sb, newContent.String()); err != nil {
		return fmt.Sprintf("Error al escribir users.txt: %v", err)
	}

	return fmt.Sprintf("Usuario %s eliminado exitosamente", user)
}

// CHGRP: Cambia el grupo de un usuario
func chgrp(params map[string]string) string {
	user, hasUser := params["user"]
	grp, hasGrp := params["grp"]
	if !hasUser || !hasGrp {
		return "Error: Parámetros -user y -grp son obligatorios"
	}

	if len(user) > 10 || len(grp) > 10 {
		return "Error: Usuario y grupo no deben exceder 10 caracteres"
	}

	if currentSession == nil {
		return "Error: No hay sesión activa"
	}
	if currentSession.Username != "root" {
		return "Error: Solo root puede cambiar grupos"
	}

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

	usersContent, err := readUsersTxt(file, sb)
	if err != nil {
		return fmt.Sprintf("Error al leer users.txt: %v", err)
	}

	lines := strings.Split(usersContent, "\n")
	groupExists := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 && parts[1] == "G" && strings.TrimSpace(parts[2]) == grp {
			groupExists = true
			break
		}
	}
	if !groupExists {
		return fmt.Sprintf("Error: El grupo %s no existe", grp)
	}

	var newContent strings.Builder
	userFound := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 4 && parts[1] == "U" && strings.TrimSpace(parts[3]) == user {
			newContent.WriteString(fmt.Sprintf("%s,U,%s,%s,%s\n", parts[0], grp, parts[3], parts[4]))
			userFound = true
		} else {
			newContent.WriteString(line + "\n")
		}
	}

	if !userFound {
		return fmt.Sprintf("Error: El usuario %s no existe", user)
	}

	if err := writeUsersTxt(file, sb, newContent.String()); err != nil {
		return fmt.Sprintf("Error al escribir users.txt: %v", err)
	}

	return fmt.Sprintf("Grupo de %s cambiado a %s exitosamente", user, grp)
}

// Funciones auxiliares
func readInode(file *os.File, sb Superblock, inodeIndex int32) (Inode, error) {
	var inode Inode
	file.Seek(int64(sb.SInodeStart+inodeIndex*sb.SInodeSize), 0)
	if err := binary.Read(file, binary.LittleEndian, &inode); err != nil {
		return Inode{}, err
	}
	return inode, nil
}

func readFolderBlock(file *os.File, sb Superblock, blockIndex int32) (FolderBlock, error) {
	var block FolderBlock
	file.Seek(int64(sb.SBlockStart+blockIndex*sb.SBlockSize), 0)
	if err := binary.Read(file, binary.LittleEndian, &block); err != nil {
		return FolderBlock{}, err
	}
	return block, nil
}

func readFileBlock(file *os.File, sb Superblock, blockIndex int32) (FileBlock, error) {
	var block FileBlock
	file.Seek(int64(sb.SBlockStart+blockIndex*sb.SBlockSize), 0)
	if err := binary.Read(file, binary.LittleEndian, &block); err != nil {
		return FileBlock{}, err
	}
	return block, nil
}

func readUsersTxt(file *os.File, sb Superblock) (string, error) {
	inode, err := readInode(file, sb, 1)
	if err != nil {
		return "", err
	}
	if inode.IType != '1' {
		return "", fmt.Errorf("inodo de users.txt inválido")
	}
	var content strings.Builder
	bytesRemaining := inode.ISize
	blockSize := sb.SBlockSize // 64 bytes

	fmt.Printf("Leyendo users.txt: iSize=%d, IBlock=%v\n", inode.ISize, inode.IBlock)

	for _, blockIndex := range inode.IBlock {
		if blockIndex == -1 {
			continue
		}
		if bytesRemaining <= 0 {
			break
		}

		fileBlock, err := readFileBlock(file, sb, blockIndex)
		if err != nil {
			return "", err
		}

		// Determinar cuántos bytes leer de este bloque
		bytesToRead := blockSize
		if bytesRemaining < blockSize {
			bytesToRead = bytesRemaining
		}

		// Leer solo los bytes necesarios y convertirlos a string
		blockContent := fileBlock.BContent[:bytesToRead]
		for i := int32(0); i < bytesToRead; i++ {
			if blockContent[i] == 0 {
				bytesToRead = i
				break
			}
		}
		content.Write(blockContent[:bytesToRead])
		bytesRemaining -= bytesToRead
	}

	return content.String(), nil
}

func writeUsersTxt(file *os.File, sb Superblock, content string) error {
	inode, err := readInode(file, sb, 1)
	if err != nil {
		return err
	}
	if inode.IType != '1' {
		return fmt.Errorf("inodo de users.txt inválido")
	}

	numBlocks := int32(math.Ceil(float64(len(content)) / 64.0))
	if numBlocks > 15 {
		return fmt.Errorf("contenido de users.txt excede capacidad")
	}

	// Leer bitmap de bloques
	bitmapBlocks := make([]byte, sb.SBlocksCount)
	file.Seek(int64(sb.SBmBlockStart), 0)
	_, err = file.Read(bitmapBlocks)
	if err != nil {
		return fmt.Errorf("error al leer bitmap de bloques: %v", err)
	}

	// Liberar bloques anteriores
	for i, blockIndex := range inode.IBlock {
		if blockIndex != -1 && blockIndex < sb.SBlocksCount {
			bitmapBlocks[blockIndex] = 0
			inode.IBlock[i] = -1
		}
	}

	// Asignar nuevos bloques
	currentBlock := int32(2) // Comenzar después de los bloques iniciales
	for i := int32(0); i < numBlocks; i++ {
		for currentBlock < sb.SBlocksCount && bitmapBlocks[currentBlock] == 1 {
			currentBlock++
		}
		if currentBlock >= sb.SBlocksCount {
			return fmt.Errorf("no hay bloques libres")
		}
		inode.IBlock[i] = currentBlock
		bitmapBlocks[currentBlock] = 1
	}

	// Escribir bloques con contenido
	contentBytes := []byte(content)
	for i := int32(0); i < numBlocks; i++ {
		var block FileBlock
		for j := range block.BContent {
			block.BContent[j] = 0
		}
		start := i * 64
		end := start + 64
		if end > int32(len(contentBytes)) {
			end = int32(len(contentBytes))
		}
		copy(block.BContent[:], contentBytes[start:end])
		file.Seek(int64(sb.SBlockStart+inode.IBlock[i]*sb.SBlockSize), 0)
		if err := binary.Write(file, binary.LittleEndian, &block); err != nil {
			return fmt.Errorf("error al escribir bloque %d: %v", inode.IBlock[i], err)
		}
	}

	// Actualizar inodo
	inode.ISize = int32(len(content))
	fecha := time.Now().Format("2006-01-02 15:04:05")
	copy(inode.IMtime[:], fecha)
	file.Seek(int64(sb.SInodeStart+sb.SInodeSize), 0)
	if err := binary.Write(file, binary.LittleEndian, &inode); err != nil {
		return fmt.Errorf("error al escribir inodo: %v", err)
	}

	// Escribir bitmap de bloques
	file.Seek(int64(sb.SBmBlockStart), 0)
	if _, err := file.Write(bitmapBlocks); err != nil {
		return fmt.Errorf("error al escribir bitmap de bloques: %v", err)
	}

	// Actualizar superbloque
	file.Seek(int64(sb.SBmInodeStart-sb.SInodeSize), 0)
	var sbUpdated Superblock
	if err := binary.Read(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Errorf("error al leer superbloque para actualizar: %v", err)
	}
	sbUpdated.SFreeBlocksCount = sb.SBlocksCount - numBlocks
	file.Seek(int64(sb.SBmInodeStart-sb.SInodeSize), 0)
	if err := binary.Write(file, binary.LittleEndian, &sbUpdated); err != nil {
		return fmt.Errorf("error al escribir superbloque: %v", err)
	}

	fmt.Printf("Escrito users.txt: iSize=%d, IBlock=%v, Content=%s\n", inode.ISize, inode.IBlock, content)

	return nil
}
