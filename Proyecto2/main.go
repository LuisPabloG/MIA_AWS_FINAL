package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/cors"
)

// MBR representa el Master Boot Record, que almacena información del disco
type MBR struct {
	MbrTamano     int32        // Tamaño total del disco en bytes
	MbrFecha      [19]byte     // Fecha de creación del disco
	MbrDskSig     int32        // Firma única del disco
	DskFit        byte         // Tipo de ajuste (B=Best, F=First, W=Worst)
	MbrPartitions [4]Partition // Arreglo de 4 particiones
}

// Partition representa una partición en el disco
type Partition struct {
	PartStatus byte     // Estado (1=activa, 0=inactiva)
	PartType   byte     // Tipo (P=primaria, E=extendida)
	PartFit    byte     // Ajuste (B=Best, F=First, W=Worst)
	PartStart  int32    // Byte inicial de la partición
	PartSize   int32    // Tamaño en bytes
	PartName   [16]byte // Nombre de la partición
	PartCorrel int32    // Correlativo para ID
	PartID     [4]byte  // Identificador único
}

// EBR representa el Extended Boot Record, usado para particiones lógicas
type EBR struct {
	PartMount  byte     // Indicador de montaje (1=montada, 0=no montada)
	PartFit    byte     // Ajuste (B=Best, F=First, W=Worst)
	PartStart  int32    // Byte inicial
	PartSize   int32    // Tamaño en bytes
	PartNext   int32    // Siguiente EBR (-1 si no hay)
	PartName   [16]byte // Nombre de la partición
	PartCorrel int32    // Correlativo para ID
	PartID     [4]byte  // Identificador único
}

// MountedPartition representa una partición montada en memoria
type MountedPartition struct {
	ID        string // ID único (ej. 291A)
	Path      string // Ruta del archivo .mia
	Name      string // Nombre de la partición
	Correl    int    // Correlativo dentro del disco
	DiskOrder byte   // Letra del disco (A, B, C, ...)
}

var mountedPartitions []MountedPartition // Lista de particiones montadas
var carnet = "202200129"                 // Carnet para generar IDs

func main() {
	// Configurar el router
	mux := http.NewServeMux()
	mux.HandleFunc("/partitions", manejarParticiones)
	mux.HandleFunc("/execute", manejarEjecucion)

	// Configurar CORS
	corsHandler := cors.New(cors.Options{
		AllowedOrigins: []string{
			"http://localhost:3000",
			"http://localhost:8080",
			"http://18.119.17.227",
			"http://18.119.17.227:3000",
			"http://mia-202200129.s3-website.us-east-2.amazonaws.com",
		},
		AllowedMethods:   []string{http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: false,
		MaxAge:           3600,
	}).Handler(mux)

	// Iniciar el servidor
	fmt.Println("Servidor iniciado en http://18.119.17.227:8080")
	if err := http.ListenAndServe(":8080", corsHandler); err != nil {
		fmt.Printf("Error al iniciar el servidor: %v\n", err)
	}
}

// manejarEjecucion procesa las solicitudes POST del frontend
func manejarEjecucion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodOptions {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	var entrada struct {
		Comandos string `json:"comandos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&entrada); err != nil {
		responder(w, fmt.Sprintf("Error al leer el cuerpo: %v", err), http.StatusBadRequest)
		return
	}

	if entrada.Comandos == "" {
		responder(w, "Error: No se proporcionaron comandos", http.StatusBadRequest)
		return
	}

	var salida strings.Builder
	for _, cmd := range strings.Split(entrada.Comandos, "\n") {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			salida.WriteString("\n")
			continue
		}
		if strings.HasPrefix(cmd, "#") {
			salida.WriteString(cmd + "\n")
			continue
		}
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}
		command := parts[0]
		params := parseParameters(parts[1:])
		resultado := ejecutarComando(command, params)
		salida.WriteString(resultado + "\n")
	}

	responder(w, salida.String(), http.StatusOK)
}

// manejarParticiones devuelve las particiones montadas, agrupadas por disco
func manejarParticiones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodOptions {
		http.Error(w, "Método no permitido", http.StatusMethodNotAllowed)
		return
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	var entrada struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&entrada); err != nil {
		responder(w, fmt.Sprintf("Error al leer el cuerpo: %v", err), http.StatusBadRequest)
		return
	}

	// Agrupar particiones por disco
	type Disk struct {
		Path       string             `json:"path"`
		Partitions []MountedPartition `json:"partitions"`
	}
	var disks []Disk
	diskMap := make(map[string][]MountedPartition)

	for _, mp := range mountedPartitions {
		if entrada.Path == "" || mp.Path == entrada.Path {
			diskMap[mp.Path] = append(diskMap[mp.Path], mp)
		}
	}

	for path, partitions := range diskMap {
		disks = append(disks, Disk{Path: path, Partitions: partitions})
	}

	respuesta := map[string]interface{}{
		"disks": disks,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(respuesta)
}

// responder envía una respuesta JSON al frontend
func responder(w http.ResponseWriter, mensaje string, status int) {
	respuesta := map[string]interface{}{
		"salida": mensaje,
	}
	if strings.HasPrefix(mensaje, "Error") {
		respuesta["error"] = mensaje
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(respuesta)
}

// ejecutarComando analiza y ejecuta un comando
func ejecutarComando(command string, params map[string]string) string {
	var salida strings.Builder
	switch strings.ToUpper(command) {
	case "MKDISK":
		return mkdisk(params)
	case "RMDISK":
		return rmdisk(params)
	case "FDISK":
		return fdisk(params)
	case "MOUNT":
		return mount(params)
	case "MKFS":
		return mkfs(params)
	case "MKFILE":
		return mkfile(params)
	case "MKDIR":
		return mkdir(params)
	case "CAT":
		return cat(params)
	case "LOGIN":
		return login(params)
	case "LOGOUT":
		return logout(params)
	case "MKGRP":
		return mkgrp(params)
	case "RMGRP":
		return rmgrp(params)
	case "MKUSR":
		return mkusr(params)
	case "RMUSR":
		return rmusr(params)
	case "CHGRP":
		return chgrp(params)
	case "MOUNTED":
		mounted(&salida)
		return salida.String()
	case "LS":
		return ls(params)
	case "REMOVE":
		return remove(params)
	case "COPY":
		return copyMap(params)
	case "MOVE":
		return move(params)
	case "FIND":
		return find(params)
	case "CHOWN":
		return chown(params)
	case "CHMOD":
		return chmod(params)
	case "EDIT":
		return edit(params)
	case "RENAME":
		return rename(params)
	case "UNMOUNT":
		return unmount(params)
	case "RECOVERY":
		return recovery(params)
	case "LOSS":
		return loss(params)
	default:
		return fmt.Sprintf("Comando %s no reconocido", command)
	}
}

// parseParameters convierte argumentos en un mapa de clave-valor
func parseParameters(args []string) map[string]string {
	params := make(map[string]string)
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			parts := strings.SplitN(arg[1:], "=", 2)
			if len(parts) == 2 {
				key := strings.ToLower(parts[0])
				value := strings.Trim(parts[1], "\"")
				params[key] = value
			}
		}
	}
	return params
}

// mkdisk crea un nuevo disco virtual (.mia)
func mkdisk(params map[string]string) string {
	var salida strings.Builder
	sizeStr, hasSize := params["size"]
	path, hasPath := params["path"]
	unit := params["unit"]
	fit := params["fit"]

	if !hasSize || !hasPath {
		salida.WriteString("Error: Parámetros -size y -path son obligatorios")
		return salida.String()
	}

	size, err := parseSize(sizeStr, unit)
	if err != nil {
		salida.WriteString(err.Error())
		return salida.String()
	}

	if size <= 0 {
		salida.WriteString("Error: El tamaño debe ser mayor que cero")
		return salida.String()
	}

	fitByte := byte('F')
	if fit != "" {
		switch strings.ToUpper(fit) {
		case "BF":
			fitByte = 'B'
		case "FF":
			fitByte = 'F'
		case "WF":
			fitByte = 'W'
		default:
			salida.WriteString("Error: Valor de -fit no válido")
			return salida.String()
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		salida.WriteString(fmt.Sprintf("Error al crear directorios: %v", err))
		return salida.String()
	}

	file, err := os.Create(path)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error al crear disco: %v", err))
		return salida.String()
	}
	defer file.Close()

	buffer := make([]byte, 1024)
	for i := 0; i < int(size)/1024; i++ {
		if _, err := file.Write(buffer); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir disco: %v", err))
			return salida.String()
		}
	}
	remaining := int(size) % 1024
	if remaining > 0 {
		if _, err := file.Write(buffer[:remaining]); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir disco: %v", err))
			return salida.String()
		}
	}

	mbr := MBR{
		MbrTamano: int32(size),
		MbrDskSig: int32(rand.Intn(1000000)),
		DskFit:    fitByte,
	}
	fecha := time.Now().Format("2006-01-02 15:04:05")
	copy(mbr.MbrFecha[:], fecha[:19])

	for i := range mbr.MbrPartitions {
		mbr.MbrPartitions[i] = Partition{
			PartStatus: '0',
			PartCorrel: -1,
		}
	}

	if err := writeMBR(file, &mbr); err != nil {
		salida.WriteString(fmt.Sprintf("Error al escribir MBR: %v", err))
		return salida.String()
	}

	salida.WriteString(fmt.Sprintf("Disco creado exitosamente: %s", path))
	return salida.String()
}

// rmdisk elimina un disco virtual
func rmdisk(params map[string]string) string {
	var salida strings.Builder
	path, hasPath := params["path"]
	if !hasPath {
		salida.WriteString("Error: Parámetro -path es obligatorio")
		return salida.String()
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		salida.WriteString(fmt.Sprintf("Error: El archivo %s no existe", path))
		return salida.String()
	}

	if err := os.Remove(path); err != nil {
		salida.WriteString(fmt.Sprintf("Error al eliminar disco: %v", err))
		return salida.String()
	}

	// Limpiar todas las particiones montadas para este disco
	newMounted := []MountedPartition{}
	for _, mp := range mountedPartitions {
		if mp.Path != path {
			newMounted = append(newMounted, mp)
		}
	}
	mountedPartitions = newMounted

	salida.WriteString(fmt.Sprintf("Disco eliminado exitosamente: %s", path))
	return salida.String()
}

// fdisk crea una partición en el disco
// fdisk crea, modifica o elimina una partición en el disco
func fdisk(params map[string]string) string {
	var salida strings.Builder
	path, hasPath := params["path"]
	name, hasName := params["name"]

	if !hasPath || !hasName {
		salida.WriteString("Error: Parámetros -path y -name son obligatorios")
		return salida.String()
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error al abrir disco: %v", err))
		return salida.String()
	}
	defer file.Close()

	mbr, err := readMBR(file)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error al leer MBR: %v", err))
		return salida.String()
	}

	// Verificar si es operación ADD
	if addStr, hasAdd := params["add"]; hasAdd {
		addSize, err := strconv.Atoi(addStr)
		if err != nil {
			salida.WriteString(fmt.Sprintf("Error: Tamaño no válido: %v", err))
			return salida.String()
		}

		// Buscar la partición por nombre en particiones primarias/extendidas
		found := false
		var partIndex int

		for i, part := range mbr.MbrPartitions {
			if part.PartStatus == '1' && strings.Trim(string(part.PartName[:]), "\x00") == name {
				found = true
				partIndex = i
				break
			}
		}

		// Si no se encuentra en particiones primarias/extendidas, buscar en lógicas
		if !found {
			for _, part := range mbr.MbrPartitions {
				if part.PartType == 'E' && part.PartStatus == '1' {
					_, err = file.Seek(int64(part.PartStart), 0)
					if err != nil {
						continue
					}

					for {
						var currentEBR EBR
						if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
							break
						}

						if currentEBR.PartSize > 0 && strings.Trim(string(currentEBR.PartName[:]), "\x00") == name {
							// Verificar si hay espacio disponible para aumentar la partición lógica
							availableSpace := int32(0)
							if currentEBR.PartNext != -1 {
								availableSpace = currentEBR.PartNext - (currentEBR.PartStart + currentEBR.PartSize)
							} else {
								// Si es la última partición lógica, verificar contra el límite de la partición extendida
								availableSpace = part.PartStart + part.PartSize - (currentEBR.PartStart + currentEBR.PartSize)
							}

							if availableSpace < int32(addSize) {
								salida.WriteString(fmt.Sprintf("Error: No hay suficiente espacio para aumentar la partición lógica %s", name))
								return salida.String()
							}

							// Aumentar tamaño de la partición lógica
							currentEBR.PartSize += int32(addSize)

							// Escribir EBR actualizado
							_, err = file.Seek(int64(currentEBR.PartStart), 0)
							if err != nil {
								salida.WriteString(fmt.Sprintf("Error al posicionar para actualizar EBR: %v", err))
								return salida.String()
							}
							if err := binary.Write(file, binary.LittleEndian, &currentEBR); err != nil {
								salida.WriteString(fmt.Sprintf("Error al actualizar EBR: %v", err))
								return salida.String()
							}

							salida.WriteString(fmt.Sprintf("Partición lógica %s redimensionada exitosamente", name))
							return salida.String()
						}

						if currentEBR.PartNext == -1 {
							break
						}
						_, err = file.Seek(int64(currentEBR.PartNext), 0)
						if err != nil {
							break
						}
					}
				}
			}
		}

		if !found {
			salida.WriteString(fmt.Sprintf("Error: Partición %s no encontrada", name))
			return salida.String()
		}

		// Verificar si hay espacio disponible para la ampliación
		var nextPartStart int32 = mbr.MbrTamano
		for i, part := range mbr.MbrPartitions {
			if i != partIndex && part.PartStatus == '1' && part.PartStart > mbr.MbrPartitions[partIndex].PartStart {
				if part.PartStart < nextPartStart {
					nextPartStart = part.PartStart
				}
			}
		}

		availableSpace := nextPartStart - (mbr.MbrPartitions[partIndex].PartStart + mbr.MbrPartitions[partIndex].PartSize)
		if availableSpace < int32(addSize) {
			salida.WriteString(fmt.Sprintf("Error: No hay suficiente espacio para aumentar la partición %s", name))
			return salida.String()
		}

		// Aumentar tamaño de la partición
		mbr.MbrPartitions[partIndex].PartSize += int32(addSize)

		// Escribir MBR actualizado
		if err := writeMBR(file, mbr); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir MBR: %v", err))
			return salida.String()
		}

		salida.WriteString(fmt.Sprintf("Partición %s redimensionada exitosamente", name))
		return salida.String()
	}

	// Verificar si es operación DELETE
	if deleteType, hasDelete := params["delete"]; hasDelete {
		if deleteType != "full" {
			salida.WriteString("Error: Valor de -delete debe ser 'full'")
			return salida.String()
		}

		// Buscar la partición por nombre
		found := false
		var partIndex int

		for i, part := range mbr.MbrPartitions {
			if part.PartStatus == '1' && strings.Trim(string(part.PartName[:]), "\x00") == name {
				found = true
				partIndex = i
				break
			}
		}

		// Si no se encuentra en primarias/extendidas, buscar en lógicas
		var extendedIndex int = -1
		if !found {
			for i, part := range mbr.MbrPartitions {
				if part.PartType == 'E' && part.PartStatus == '1' {
					extendedIndex = i
					_, err = file.Seek(int64(part.PartStart), 0)
					if err != nil {
						continue
					}

					var prevEBRPos int64 = -1

					for {
						currentPos, _ := file.Seek(0, os.SEEK_CUR)
						var currentEBR EBR
						if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
							break
						}

						if currentEBR.PartSize > 0 && strings.Trim(string(currentEBR.PartName[:]), "\x00") == name {
							// Desmontar la partición si está montada
							var idToUnmount string
							newMounted := []MountedPartition{}
							for _, mp := range mountedPartitions {
								if mp.Path == path && mp.Name == name {
									idToUnmount = mp.ID
								} else {
									newMounted = append(newMounted, mp)
								}
							}
							mountedPartitions = newMounted

							// Si no es la primera partición lógica, actualizar el EBR anterior
							if prevEBRPos != -1 {
								_, err = file.Seek(prevEBRPos, 0)
								if err != nil {
									salida.WriteString(fmt.Sprintf("Error al posicionar para actualizar EBR anterior: %v", err))
									return salida.String()
								}
								var prevEBR EBR
								if err := binary.Read(file, binary.LittleEndian, &prevEBR); err != nil {
									salida.WriteString(fmt.Sprintf("Error al leer EBR anterior: %v", err))
									return salida.String()
								}

								prevEBR.PartNext = currentEBR.PartNext

								_, err = file.Seek(prevEBRPos, 0)
								if err != nil {
									salida.WriteString(fmt.Sprintf("Error al posicionar para escribir EBR actualizado: %v", err))
									return salida.String()
								}
								if err := binary.Write(file, binary.LittleEndian, &prevEBR); err != nil {
									salida.WriteString(fmt.Sprintf("Error al actualizar EBR anterior: %v", err))
									return salida.String()
								}
							} else {
								// Si es la primera, crear un EBR vacío
								emptyEBR := EBR{
									PartMount:  '0',
									PartFit:    currentEBR.PartFit,
									PartStart:  currentEBR.PartStart,
									PartSize:   0,
									PartNext:   currentEBR.PartNext,
									PartCorrel: -1,
								}

								_, err = file.Seek(int64(currentEBR.PartStart), 0)
								if err != nil {
									salida.WriteString(fmt.Sprintf("Error al posicionar para escribir EBR vacío: %v", err))
									return salida.String()
								}
								if err := binary.Write(file, binary.LittleEndian, &emptyEBR); err != nil {
									salida.WriteString(fmt.Sprintf("Error al escribir EBR vacío: %v", err))
									return salida.String()
								}
							}

							result := fmt.Sprintf("Partición lógica %s eliminada exitosamente", name)
							if idToUnmount != "" {
								result += fmt.Sprintf(" (desmontada ID %s)", idToUnmount)
							}
							salida.WriteString(result)
							return salida.String()
						}

						if currentEBR.PartNext == -1 {
							break
						}
						prevEBRPos = currentPos
						_, err = file.Seek(int64(currentEBR.PartNext), 0)
						if err != nil {
							break
						}
					}
				}
			}
		}

		if !found {
			salida.WriteString(fmt.Sprintf("Error: Partición %s no encontrada", name))
			return salida.String()
		}

		// Desmontar la partición si está montada
		var idToUnmount string
		newMounted := []MountedPartition{}
		for _, mp := range mountedPartitions {
			if mp.Path == path && mp.Name == name {
				idToUnmount = mp.ID
			} else {
				newMounted = append(newMounted, mp)
			}
		}
		mountedPartitions = newMounted

		// Liberar la partición
		if extendedIndex == partIndex {
			// Si eliminamos una partición extendida, también debemos eliminar todas sus particiones lógicas
			for _, mp := range mountedPartitions {
				if mp.Path == path {
					// Validar que es una partición lógica dentro de esta extendida
					file, err = os.OpenFile(path, os.O_RDWR, 0644)
					if err != nil {
						continue
					}
					defer file.Close()

					_, err = file.Seek(int64(mbr.MbrPartitions[partIndex].PartStart), 0)
					if err != nil {
						continue
					}

					isInThisExtended := false
					for {
						var currentEBR EBR
						if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
							break
						}

						if strings.Trim(string(currentEBR.PartName[:]), "\x00") == mp.Name {
							isInThisExtended = true
							break
						}

						if currentEBR.PartNext == -1 {
							break
						}
						_, err = file.Seek(int64(currentEBR.PartNext), 0)
						if err != nil {
							break
						}
					}

					if isInThisExtended {
						newMounted = []MountedPartition{}
						for _, mp2 := range mountedPartitions {
							if mp2.ID != mp.ID {
								newMounted = append(newMounted, mp2)
							}
						}
						mountedPartitions = newMounted
					}
				}
			}
		}

		mbr.MbrPartitions[partIndex].PartStatus = '0'
		mbr.MbrPartitions[partIndex].PartSize = 0

		// Escribir MBR actualizado
		if err := writeMBR(file, mbr); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir MBR: %v", err))
			return salida.String()
		}

		result := fmt.Sprintf("Partición %s eliminada exitosamente", name)
		if idToUnmount != "" {
			result += fmt.Sprintf(" (desmontada ID %s)", idToUnmount)
		}
		salida.WriteString(result)
		return salida.String()
	}

	// Continuar con la creación de partición
	sizeStr, hasSize := params["size"]
	unit := params["unit"]
	partType := strings.ToUpper(params["type"])
	fit := params["fit"]

	if partType == "" {
		partType = "P"
	}
	if partType != "P" && partType != "E" && partType != "L" {
		salida.WriteString("Error: Valor de -type no válido")
		return salida.String()
	}

	fitByte := byte('W')
	if fit != "" {
		switch strings.ToUpper(fit) {
		case "BF":
			fitByte = 'B'
		case "FF":
			fitByte = 'F'
		case "WF":
			fitByte = 'W'
		default:
			salida.WriteString(fmt.Sprintf("Error: Valor de -fit no válido: %s", fit))
			return salida.String()
		}
	}

	primaryCount := 0
	extendedCount := 0
	for _, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' {
			if part.PartType == 'E' {
				extendedCount++
			} else {
				primaryCount++
			}
		}
	}

	if primaryCount+extendedCount >= 4 && partType != "L" {
		salida.WriteString("Error: No se pueden crear más particiones primarias o extendidas")
		return salida.String()
	}
	if extendedCount >= 1 && partType == "E" {
		salida.WriteString("Error: Solo puede haber una partición extendida por disco")
		return salida.String()
	}
	if partType == "L" && extendedCount == 0 {
		salida.WriteString("Error: No existe partición extendida para crear la lógica")
		return salida.String()
	}

	// Verificar si el nombre ya existe
	for _, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' && strings.Trim(string(part.PartName[:]), "\x00") == name {
			salida.WriteString(fmt.Sprintf("Error: El nombre %s ya existe", name))
			return salida.String()
		}
	}

	// Verificar si el nombre existe en particiones lógicas
	for _, part := range mbr.MbrPartitions {
		if part.PartType == 'E' && part.PartStatus == '1' {
			_, err = file.Seek(int64(part.PartStart), 0)
			if err != nil {
				continue
			}

			for {
				var currentEBR EBR
				if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
					break
				}

				if currentEBR.PartSize > 0 {
					ebrName := strings.Trim(string(currentEBR.PartName[:]), "\x00")
					if ebrName == name {
						salida.WriteString(fmt.Sprintf("Error: El nombre %s ya existe en una partición lógica", name))
						return salida.String()
					}
				}

				if currentEBR.PartNext == -1 {
					break
				}
				_, err = file.Seek(int64(currentEBR.PartNext), 0)
				if err != nil {
					break
				}
			}
		}
	}

	var size int32
	if hasSize {
		sizeVal, err := parseSize(sizeStr, unit)
		if err != nil {
			salida.WriteString(fmt.Sprintf("Error: Tamaño no válido: %v", err))
			return salida.String()
		}
		size = int32(sizeVal)
		if size <= 0 {
			salida.WriteString("Error: El tamaño debe ser mayor a 0")
			return salida.String()
		}
	} else if !hasSize && params["delete"] == "" && params["add"] == "" {
		salida.WriteString("Error: Se requiere -size, -delete o -add")
		return salida.String()
	}

	// Crear partición lógica en disco
	if partType == "L" {
		extendedIndex := -1
		var extendedPartition Partition
		for i, part := range mbr.MbrPartitions {
			if part.PartType == 'E' && part.PartStatus == '1' {
				extendedIndex = i
				extendedPartition = part
				break
			}
		}

		if extendedIndex == -1 {
			salida.WriteString("Error: No se encontró partición extendida")
			return salida.String()
		}

		_, err = file.Seek(int64(extendedPartition.PartStart), 0)
		if err != nil {
			salida.WriteString(fmt.Sprintf("Error al posicionar en partición extendida: %v", err))
			return salida.String()
		}

		var firstEBR EBR
		err = binary.Read(file, binary.LittleEndian, &firstEBR)
		if err != nil || firstEBR.PartStart == 0 {
			// Crear EBR inicial en la partición extendida
			firstEBR = EBR{
				PartMount:  '0',
				PartFit:    fitByte,
				PartStart:  extendedPartition.PartStart,
				PartSize:   0, // El primer EBR no tiene tamaño hasta que se use
				PartNext:   -1,
				PartCorrel: -1,
			}

			// Volver al inicio de la partición extendida
			_, err = file.Seek(int64(extendedPartition.PartStart), 0)
			if err != nil {
				salida.WriteString(fmt.Sprintf("Error al posicionar para escribir EBR inicial: %v", err))
				return salida.String()
			}

			// Escribir el EBR vacío
			if err := binary.Write(file, binary.LittleEndian, &firstEBR); err != nil {
				salida.WriteString(fmt.Sprintf("Error al escribir EBR inicial: %v", err))
				return salida.String()
			}
		}

		// Si el primer EBR está vacío, usarlo
		if firstEBR.PartSize == 0 {
			// Asegurar que hay espacio suficiente
			if extendedPartition.PartSize < int32(size)+int32(binary.Size(EBR{})) {
				salida.WriteString("Error: No hay espacio suficiente en la partición extendida")
				return salida.String()
			}

			// Actualizar el primer EBR con los datos de la partición
			firstEBR.PartSize = int32(size)
			firstEBR.PartFit = fitByte
			copy(firstEBR.PartName[:], name)

			// Volver al inicio de la partición extendida
			_, err = file.Seek(int64(extendedPartition.PartStart), 0)
			if err != nil {
				salida.WriteString(fmt.Sprintf("Error al posicionar para escribir EBR: %v", err))
				return salida.String()
			}

			// Escribir el EBR actualizado
			if err := binary.Write(file, binary.LittleEndian, &firstEBR); err != nil {
				salida.WriteString(fmt.Sprintf("Error al escribir EBR: %v", err))
				return salida.String()
			}

			salida.WriteString(fmt.Sprintf("Partición lógica %s creada exitosamente", name))
			return salida.String()
		}

		// Si el primer EBR ya está en uso, buscar espacio en la lista enlazada de EBRs
		currentEBR := firstEBR
		var prevEBRPos int64 = int64(extendedPartition.PartStart)

		for {
			// Si current no tiene next, podemos añadir uno nuevo al final
			if currentEBR.PartNext == -1 {
				// Calcular donde iría el nuevo EBR
				newEBRPos := int64(currentEBR.PartStart) + int64(currentEBR.PartSize) + int64(binary.Size(EBR{}))

				// Verificar que hay espacio suficiente
				spaceAvailable := int64(extendedPartition.PartStart) + int64(extendedPartition.PartSize) - newEBRPos
				if spaceAvailable < int64(size)+int64(binary.Size(EBR{})) {
					salida.WriteString("Error: No hay espacio suficiente en la partición extendida")
					return salida.String()
				}

				// Crear el nuevo EBR
				newEBR := EBR{
					PartMount:  '0',
					PartFit:    fitByte,
					PartStart:  int32(newEBRPos),
					PartSize:   int32(size),
					PartNext:   -1,
					PartName:   [16]byte{},
					PartCorrel: -1,
				}
				copy(newEBR.PartName[:], name)

				// Escribir el nuevo EBR
				_, err = file.Seek(newEBRPos, 0)
				if err != nil {
					salida.WriteString(fmt.Sprintf("Error al posicionar para escribir nuevo EBR: %v", err))
					return salida.String()
				}
				if err := binary.Write(file, binary.LittleEndian, &newEBR); err != nil {
					salida.WriteString(fmt.Sprintf("Error al escribir nuevo EBR: %v", err))
					return salida.String()
				}

				// Actualizar el EBR anterior para que apunte al nuevo
				currentEBR.PartNext = int32(newEBRPos)
				_, err = file.Seek(prevEBRPos, 0)
				if err != nil {
					salida.WriteString(fmt.Sprintf("Error al posicionar para actualizar EBR anterior: %v", err))
					return salida.String()
				}
				if err := binary.Write(file, binary.LittleEndian, &currentEBR); err != nil {
					salida.WriteString(fmt.Sprintf("Error al actualizar EBR anterior: %v", err))
					return salida.String()
				}

				salida.WriteString(fmt.Sprintf("Partición lógica %s creada exitosamente", name))
				return salida.String()
			}

			// Buscar espacio entre EBRs actuales
			nextEBRPos := int64(currentEBR.PartNext)
			spaceStart := int64(currentEBR.PartStart) + int64(currentEBR.PartSize) + int64(binary.Size(EBR{}))
			spaceAvailable := nextEBRPos - spaceStart

			if spaceAvailable >= int64(size)+int64(binary.Size(EBR{})) {
				// Hay espacio para insertar una partición aquí
				newEBR := EBR{
					PartMount:  '0',
					PartFit:    fitByte,
					PartStart:  int32(spaceStart),
					PartSize:   int32(size),
					PartNext:   currentEBR.PartNext,
					PartName:   [16]byte{},
					PartCorrel: -1,
				}
				copy(newEBR.PartName[:], name)

				// Escribir el nuevo EBR
				_, err = file.Seek(spaceStart, 0)
				if err != nil {
					salida.WriteString(fmt.Sprintf("Error al posicionar para escribir nuevo EBR: %v", err))
					return salida.String()
				}
				if err := binary.Write(file, binary.LittleEndian, &newEBR); err != nil {
					salida.WriteString(fmt.Sprintf("Error al escribir nuevo EBR: %v", err))
					return salida.String()
				}

				// Actualizar el EBR anterior para que apunte al nuevo
				currentEBR.PartNext = int32(spaceStart)
				_, err = file.Seek(prevEBRPos, 0)
				if err != nil {
					salida.WriteString(fmt.Sprintf("Error al posicionar para actualizar EBR anterior: %v", err))
					return salida.String()
				}
				if err := binary.Write(file, binary.LittleEndian, &currentEBR); err != nil {
					salida.WriteString(fmt.Sprintf("Error al actualizar EBR anterior: %v", err))
					return salida.String()
				}

				salida.WriteString(fmt.Sprintf("Partición lógica %s creada exitosamente", name))
				return salida.String()
			}

			// Avanzar al siguiente EBR
			prevEBRPos = int64(currentEBR.PartStart)
			_, err = file.Seek(int64(currentEBR.PartNext), 0)
			if err != nil {
				salida.WriteString(fmt.Sprintf("Error al posicionar al siguiente EBR: %v", err))
				return salida.String()
			}
			if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
				salida.WriteString(fmt.Sprintf("Error al leer el siguiente EBR: %v", err))
				return salida.String()
			}
		}
	}

	// Crear partición primaria o extendida
	// Encontrar espacio para la partición
	start, err := findSpace(mbr, size, fitByte)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error: %v", err))
		return salida.String()
	}

	// Encontrar partición libre en el MBR
	freeIndex := -1
	for i, part := range mbr.MbrPartitions {
		if part.PartStatus == '0' {
			freeIndex = i
			break
		}
	}
	if freeIndex == -1 {
		salida.WriteString("Error: No hay espacio en la tabla de particiones")
		return salida.String()
	}

	// Crear la partición
	mbr.MbrPartitions[freeIndex] = Partition{
		PartStatus: '1',
		PartType:   partType[0],
		PartFit:    fitByte,
		PartStart:  start,
		PartSize:   size,
		PartName:   [16]byte{},
		PartCorrel: int32(freeIndex),
	}
	copy(mbr.MbrPartitions[freeIndex].PartName[:], name)

	// Escribir MBR actualizado
	if err := writeMBR(file, mbr); err != nil {
		salida.WriteString(fmt.Sprintf("Error al escribir MBR: %v", err))
		return salida.String()
	}

	// Si es extendida, inicializar su EBR
	if partType == "E" {
		ebr := EBR{
			PartMount:  '0',
			PartFit:    'W',
			PartStart:  int32(start),
			PartSize:   0,
			PartNext:   -1,
			PartCorrel: -1,
		}
		_, err = file.Seek(int64(start), 0)
		if err != nil {
			salida.WriteString(fmt.Sprintf("Error al posicionar para escribir EBR inicial: %v", err))
			return salida.String()
		}
		if err := binary.Write(file, binary.LittleEndian, &ebr); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir EBR inicial: %v", err))
			return salida.String()
		}
	}

	salida.WriteString(fmt.Sprintf("Partición %s creada exitosamente", name))
	return salida.String()
}

// mount monta una partición
func mount(params map[string]string) string {
	var salida strings.Builder
	path, hasPath := params["path"]
	name, hasName := params["name"]

	if !hasPath || !hasName {
		salida.WriteString("Error: Parámetros -path y -name son obligatorios")
		return salida.String()
	}

	// Verificar si la partición ya está montada
	for _, mp := range mountedPartitions {
		if mp.Path == path && mp.Name == name {
			salida.WriteString(fmt.Sprintf("Error: La partición %s ya está montada con ID %s", name, mp.ID))
			return salida.String()
		}
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error al abrir disco: %v", err))
		return salida.String()
	}
	defer file.Close()

	mbr, err := readMBR(file)
	if err != nil {
		salida.WriteString(fmt.Sprintf("Error al leer MBR: %v", err))
		return salida.String()
	}

	partitionIndex := -1
	for i, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' && strings.Trim(string(part.PartName[:]), "\x00") == name {
			partitionIndex = i
			break
		}
	}

	if partitionIndex != -1 {
		// Determinar DiskOrder y Correl
		diskOrder := byte('A')
		maxCorrel := 0
		pathExists := false

		// Buscar DiskOrder existente para este disco
		for _, mp := range mountedPartitions {
			if mp.Path == path {
				pathExists = true
				diskOrder = mp.DiskOrder // Reusar el mismo DiskOrder
				if mp.Correl > maxCorrel {
					maxCorrel = mp.Correl
				}
			}
		}

		// Si el disco no tiene particiones montadas, asignar nuevo DiskOrder
		if !pathExists {
			usedOrders := make(map[byte]bool)
			for _, mp := range mountedPartitions {
				usedOrders[mp.DiskOrder] = true
			}
			for diskOrder = 'A'; diskOrder <= 'Z'; diskOrder++ {
				if !usedOrders[diskOrder] {
					break
				}
			}
			if diskOrder > 'Z' {
				salida.WriteString("Error: No hay letras disponibles para DiskOrder")
				return salida.String()
			}
		}

		correl := maxCorrel + 1
		id := fmt.Sprintf("%s%d%c", carnet[len(carnet)-2:], correl, diskOrder)

		mbr.MbrPartitions[partitionIndex].PartCorrel = int32(correl)
		copy(mbr.MbrPartitions[partitionIndex].PartID[:], id)

		if err := writeMBR(file, mbr); err != nil {
			salida.WriteString(fmt.Sprintf("Error al escribir MBR: %v", err))
			return salida.String()
		}

		mountedPartitions = append(mountedPartitions, MountedPartition{
			ID:        id,
			Path:      path,
			Name:      name,
			Correl:    correl,
			DiskOrder: diskOrder,
		})

		fmt.Printf("Montada partición: ID=%s, Path=%s, Name=%s, Correl=%d, DiskOrder=%c\n", id, path, name, correl, diskOrder)
		salida.WriteString(fmt.Sprintf("Partición %s montada exitosamente con ID %s", name, id))
		return salida.String()
	}

	for _, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' && part.PartType == 'E' {
			_, err = file.Seek(int64(part.PartStart), 0)
			if err != nil {
				salida.WriteString(fmt.Sprintf("Error al posicionar en partición extendida %s: %v", string(part.PartName[:]), err))
				continue
			}

			for {
				var currentEBR EBR
				if err := binary.Read(file, binary.LittleEndian, &currentEBR); err != nil {
					salida.WriteString(fmt.Sprintf("Error al leer EBR en posición %d: %v", part.PartStart, err))
					break
				}

				if strings.Trim(string(currentEBR.PartName[:]), "\x00") == name && currentEBR.PartSize > 0 {
					diskOrder := byte('A')
					maxCorrel := 0
					pathExists := false

					// Buscar DiskOrder existente para este disco
					for _, mp := range mountedPartitions {
						if mp.Path == path {
							pathExists = true
							diskOrder = mp.DiskOrder
							if mp.Correl > maxCorrel {
								maxCorrel = mp.Correl
							}
						}
					}

					// Asignar nuevo DiskOrder si es necesario
					if !pathExists {
						usedOrders := make(map[byte]bool)
						for _, mp := range mountedPartitions {
							usedOrders[mp.DiskOrder] = true
						}
						for diskOrder = 'A'; diskOrder <= 'Z'; diskOrder++ {
							if !usedOrders[diskOrder] {
								break
							}
						}
						if diskOrder > 'Z' {
							salida.WriteString("Error: No hay letras disponibles para DiskOrder")
							return salida.String()
						}
					}

					correl := maxCorrel + 1
					id := fmt.Sprintf("%s%d%c", carnet[len(carnet)-2:], correl, diskOrder)

					currentEBR.PartMount = '1'
					currentEBR.PartCorrel = int32(correl)
					copy(currentEBR.PartID[:], id)

					_, err = file.Seek(int64(currentEBR.PartStart), 0)
					if err != nil {
						salida.WriteString(fmt.Sprintf("Error al posicionar para actualizar EBR: %v", err))
						return salida.String()
					}
					if err := binary.Write(file, binary.LittleEndian, &currentEBR); err != nil {
						salida.WriteString(fmt.Sprintf("Error al escribir EBR: %v", err))
						return salida.String()
					}

					mountedPartitions = append(mountedPartitions, MountedPartition{
						ID:        id,
						Path:      path,
						Name:      name,
						Correl:    correl,
						DiskOrder: diskOrder,
					})

					fmt.Printf("Montada partición lógica: ID=%s, Path=%s, Name=%s, Correl=%d, DiskOrder=%c\n", id, path, name, correl, diskOrder)
					salida.WriteString(fmt.Sprintf("Partición %s montada exitosamente con ID %s", name, id))
					return salida.String()
				}

				if currentEBR.PartNext == -1 {
					break
				}
				_, err = file.Seek(int64(currentEBR.PartNext), 0)
				if err != nil {
					salida.WriteString(fmt.Sprintf("Error al posicionar al siguiente EBR (%d): %v", currentEBR.PartNext, err))
					break
				}
			}
		}
	}

	salida.WriteString(fmt.Sprintf("Error: La partición %s no existe", name))
	return salida.String()
}

// mounted muestra las particiones montadas
func mounted(salida *strings.Builder) {
	if len(mountedPartitions) == 0 {
		salida.WriteString("No hay particiones montadas")
		return
	}

	salida.WriteString("Particiones montadas:\n")
	for i, mp := range mountedPartitions {
		salida.WriteString(fmt.Sprintf("ID: %s, Path: %s, Name: %s", mp.ID, mp.Path, mp.Name))
		if i < len(mountedPartitions)-1 {
			salida.WriteString("\n")
		}
	}
}

// parseSize convierte el tamaño a bytes
func parseSize(sizeStr, unit string) (int64, error) {
	var size int64
	_, err := fmt.Sscanf(sizeStr, "%d", &size)
	if err != nil {
		return 0, fmt.Errorf("Error: El tamaño debe ser un número entero")
	}

	switch strings.ToUpper(unit) {
	case "K", "":
		size *= 1024
	case "M":
		size *= 1024 * 1024
	default:
		return 0, fmt.Errorf("Error: Unidad %s no válida", unit)
	}

	return size, nil
}

// readMBR lee el MBR del archivo
func readMBR(file *os.File) (*MBR, error) {
	_, err := file.Seek(0, 0)
	if err != nil {
		return nil, err
	}

	mbr := &MBR{}
	if err := binary.Read(file, binary.LittleEndian, mbr); err != nil {
		return nil, err
	}

	return mbr, nil
}

// writeMBR escribe el MBR en el archivo
func writeMBR(file *os.File, mbr *MBR) error {
	_, err := file.Seek(0, 0)
	if err != nil {
		return err
	}

	return binary.Write(file, binary.LittleEndian, mbr)
}

// findSpace encuentra espacio para una partición
func findSpace(mbr *MBR, size int32, fit byte) (int32, error) {
	start := int32(binary.Size(MBR{}))
	usedSpaces := make([]struct{ start, end int32 }, 0)

	for _, part := range mbr.MbrPartitions {
		if part.PartStatus == '1' {
			usedSpaces = append(usedSpaces, struct{ start, end int32 }{part.PartStart, part.PartStart + part.PartSize})
		}
	}

	for i := range usedSpaces {
		for j := i + 1; j < len(usedSpaces); j++ {
			if usedSpaces[i].start > usedSpaces[j].start {
				usedSpaces[i], usedSpaces[j] = usedSpaces[j], usedSpaces[i]
			}
		}
	}

	if fit == 'F' {
		for i := 0; i <= len(usedSpaces); i++ {
			var nextStart int32
			if i == len(usedSpaces) {
				nextStart = mbr.MbrTamano
			} else {
				nextStart = usedSpaces[i].start
			}

			available := nextStart - start
			if available >= size {
				return start, nil
			}

			if i < len(usedSpaces) {
				start = usedSpaces[i].end
			}
		}
	} else if fit == 'B' {
		bestStart := int32(-1)
		minSpace := mbr.MbrTamano + 1
		currentStart := int32(binary.Size(MBR{}))
		for i := 0; i <= len(usedSpaces); i++ {
			var nextStart int32
			if i == len(usedSpaces) {
				nextStart = mbr.MbrTamano
			} else {
				nextStart = usedSpaces[i].start
			}

			available := nextStart - currentStart
			if available >= size && available < minSpace {
				minSpace = available
				bestStart = currentStart
			}

			if i < len(usedSpaces) {
				currentStart = usedSpaces[i].end
			}
		}
		if bestStart != -1 {
			return bestStart, nil
		}
	} else if fit == 'W' {
		worstStart := int32(-1)
		maxSpace := int32(0)
		currentStart := int32(binary.Size(MBR{}))
		for i := 0; i <= len(usedSpaces); i++ {
			var nextStart int32
			if i == len(usedSpaces) {
				nextStart = mbr.MbrTamano
			} else {
				nextStart = usedSpaces[i].start
			}

			available := nextStart - currentStart
			if available >= size && available > maxSpace {
				maxSpace = available
				worstStart = currentStart
			}

			if i < len(usedSpaces) {
				currentStart = usedSpaces[i].end
			}
		}
		if worstStart != -1 {
			return worstStart, nil
		}
	}

	return 0, fmt.Errorf("Error: No hay espacio suficiente para la partición")
}

// ls: Lista el contenido de una ruta en una partición montada.
func ls(params map[string]string) string {
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
	var pathParts []string
	if path != "/" {
		pathParts, err = normalizePath(path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
	}

	// Navegar hasta la carpeta
	currentInode := int32(0) // Raíz por defecto
	if len(pathParts) > 0 {
		currentInode, err = navigateToParent(file, sb, pathParts)
		if err != nil {
			return fmt.Sprintf("Error al navegar a la ruta: %v", err)
		}
	}

	// Leer inodo de la carpeta
	inode, err := readInode(file, sb, currentInode)
	if err != nil {
		return fmt.Sprintf("Error al leer inodo %d: %v", currentInode, err)
	}
	if inode.IType != '0' {
		return fmt.Sprintf("Error: %s no es una carpeta", path)
	}

	// Listar contenido
	seen := make(map[string]bool)
	var contents []map[string]interface{}
	for _, blockIndex := range inode.IBlock {
		if blockIndex == -1 {
			continue
		}
		folderBlock, err := readFolderBlock(file, sb, blockIndex)
		if err != nil {
			return fmt.Sprintf("Error al leer bloque %d: %v", blockIndex, err)
		}
		for _, content := range folderBlock.BContent {
			name := strings.Trim(string(content.BName[:]), "\x00")
			if name == "" || name == "." || name == ".." || seen[name] {
				continue
			}
			seen[name] = true
			itemInode, err := readInode(file, sb, content.BInode)
			if err != nil {
				continue
			}
			// Verificar que el inodo sea válido
			if itemInode.IType != '0' && itemInode.IType != '1' {
				continue
			}
			contents = append(contents, map[string]interface{}{
				"name":          name,
				"type":          string(itemInode.IType),
				"size":          itemInode.ISize,
				"creation_date": strings.Trim(string(itemInode.ICtime[:]), "\x00"),
				"permissions":   fmt.Sprintf("%03d", itemInode.IPerm),
			})
		}
	}

	// Devolver resultado como JSON
	result, err := json.Marshal(contents)
	if err != nil {
		return fmt.Sprintf("Error al generar JSON: %v", err)
	}
	return string(result)
}

// stringToUpper es una función auxiliar para manejar la conversión a mayúsculas
func stringToUpper(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
