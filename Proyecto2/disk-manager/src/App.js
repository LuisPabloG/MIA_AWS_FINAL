import { useState, useRef,useEffect } from 'react';
import './App.css';

function App() {
  const [comandos, setComandos] = useState('');
  const [salida, setSalida] = useState('');
  const [estaConectado, setEstaConectado] = useState(false);
  const [usuarioActual, setUsuarioActual] = useState('');
  const [idParticionActual, setIdParticionActual] = useState('');
  const [mostrarModal, setMostrarModal] = useState(false);
  const [vista, setVista] = useState('terminal'); // terminal, diskSelector, partitionSelector, fileSystem
  const [discoSeleccionado, setDiscoSeleccionado] = useState(null);
  const [rutaActual, setRutaActual] = useState('/');
  const [usuario, setUsuario] = useState('');
  const [contrasena, setContrasena] = useState('');
  const [idParticion, setIdParticion] = useState('');
  const [error, setError] = useState('');
  const referenciaArchivo = useRef(null);

   // Determinar la URL del backend según el entorno

const BACKEND_URL = window.location.hostname.includes('18.119.17.227') || 
                   window.location.hostname === 'ec2-3-145-6-97.us-east-2.compute.amazonaws.com' ||
                   window.location.hostname.includes('s3-website') // Detectar cuando se ejecuta desde S3
                   ? 'http://18.119.17.227:8080' 
                   : 'http://localhost:8080';
  
  // Ejecutar comandos genéricos
  const ejecutarComando = async (comando) => {
    try {
      const respuesta = await fetch(`${BACKEND_URL}/execute`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ comandos: comando }),
      });
      const resultado = await respuesta.json();
      return resultado.salida || '';
    } catch (error) {
      return `Error: ${error.message}`;
    }
  };
  // Ejecutar comandos desde la terminal
  const manejarEjecucion = async () => {
    const resultado = await ejecutarComando(comandos);
    setSalida(resultado);
  };

  // Manejar carga de archivo .smia
  const manejarCargaArchivo = (evento) => {
    const archivo = evento.target.files[0];
    if (archivo && archivo.name.endsWith('.smia')) {
      const lector = new FileReader();
      lector.onload = (e) => {
        setComandos(e.target.result);
      };
      lector.readAsText(archivo);
    } else {
      setSalida('Error: Por favor, carga un archivo .smia válido');
    }
  };

  // Activar entrada de archivo
  const activarEntradaArchivo = () => {
    referenciaArchivo.current.click();
  };

  // Manejar inicio de sesión
  const manejarInicioSesion = async (e) => {
    e.preventDefault();
    setError('');

    if (!usuario || !contrasena || !idParticion) {
      setError('Por favor, completa todos los campos');
      return;
    }

    const comandoLogin = `login -user="${usuario}" -pass="${contrasena}" -id="${idParticion}"`;
    const salida = await ejecutarComando(comandoLogin);

    if (salida.includes('Sesión iniciada para')) {
      setEstaConectado(true);
      setUsuarioActual(usuario);
      setIdParticionActual(idParticion);
      setSalida(salida);
      setMostrarModal(false);
      setUsuario('');
      setContrasena('');
      setIdParticion('');
    } else {
      setError(salida || 'Error al iniciar sesión');
    }
  };

  // Manejar cierre de sesión
  const manejarCierreSesion = async () => {
    const salida = await ejecutarComando('logout');
    setSalida(salida);
    setEstaConectado(false);
    setUsuarioActual('');
    setIdParticionActual('');
    setComandos('');
    setVista('terminal');
    setDiscoSeleccionado(null);
    setRutaActual('/');
  };

  // Obtener lista de discos
  const obtenerDiscos = async () => {
    const salida = await ejecutarComando('diskinfo');
    // Suponiendo que el backend devuelve un JSON parseable
    try {
      return JSON.parse(salida) || [];
    } catch {
      return [
        { path: '/home/disk1.mia', capacity: '20MB', fit: 'FF', mounted: ['291A', '292A'] },
        { path: '/home/disk2.mia', capacity: '15MB', fit: 'BF', mounted: ['291B', '292B'] },
      ]; // Datos de ejemplo
    }
  };

  // Obtener particiones de un disco
  const obtenerParticiones = async (discoPath) => {
    const salida = await ejecutarComando(`partitioninfo -path="${discoPath}"`);
    try {
      return JSON.parse(salida) || [];
    } catch {
      return [
        { name: 'Particion1', id: '291A', size: '5000KB', fit: 'BF', status: 'Mounted' },
        { name: 'ParticionLogica1', id: '292A', size: '2000KB', fit: 'WF', status: 'Mounted' },
      ]; // Datos de ejemplo
    }
  };

  // Listar contenido de una ruta
  const listarDirectorio = async (ruta, idParticion) => {
    const comando = `ls -path="${ruta}" -id=${idParticion}`;
    const salida = await ejecutarComando(comando);
    try {
      return JSON.parse(salida) || [];
    } catch {
      return [
        { name: 'users.txt', type: 'file', permissions: 'rw-r--r--' },
        { name: 'docs', type: 'folder', permissions: 'rwx-r-xr-x' },
      ]; // Datos de ejemplo
    }
  };

  // Leer contenido de un archivo
  const leerArchivo = async (ruta, idParticion) => {
    const comando = `cat -file="${ruta}"`;
    return await ejecutarComando(comando);
  };

  // Componente para seleccionar disco
  const DiskSelector = () => {
    const [discos, setDiscos] = useState([]);

    useEffect(() => {
      obtenerDiscos().then(setDiscos);
    }, []);

    return (
      <div className="seccion-visualizador">
        <h2 className="subtitulo">Seleccionar Disco</h2>
        <div className="lista-discos">
          {discos.map((disco) => (
            <div key={disco.path} className="tarjeta-disco" onClick={() => {
              setDiscoSeleccionado(disco);
              setVista('partitionSelector');
            }}>
              <h3>{disco.path}</h3>
              <p>Capacidad: {disco.capacity}</p>
              <p>Fit: {disco.fit}</p>
              <p>Particiones Montadas: {disco.mounted.join(', ')}</p>
            </div>
          ))}
        </div>
        <div className="seccion-botones">
          <button onClick={() => setVista('terminal')} className="boton-volver">
            Volver a Terminal
          </button>
          <button onClick={manejarCierreSesion} className="boton-cerrar-sesion">
            Cerrar Sesión
          </button>
        </div>
      </div>
    );
  };

  // Componente para seleccionar partición
  const PartitionSelector = () => {
    const [particiones, setParticiones] = useState([]);

    useEffect(() => {
      if (discoSeleccionado) {
        obtenerParticiones(discoSeleccionado.path).then(setParticiones);
      }
    }, [discoSeleccionado]);

    return (
      <div className="seccion-visualizador">
        <h2 className="subtitulo">Seleccionar Partición - {discoSeleccionado?.path}</h2>
        <div className="lista-particiones">
          {particiones.map((particion) => (
            <div key={particion.id} className="tarjeta-particion" onClick={() => {
              setIdParticionActual(particion.id);
              setRutaActual('/');
              setVista('fileSystem');
            }}>
              <h3>{particion.name} ({particion.id})</h3>
              <p>Tamaño: {particion.size}</p>
              <p>Fit: {particion.fit}</p>
              <p>Estado: {particion.status}</p>
            </div>
          ))}
        </div>
        <div className="seccion-botones">
          <button onClick={() => setVista('diskSelector')} className="boton-volver">
            Volver a Discos
          </button>
          <button onClick={manejarCierreSesion} className="boton-cerrar-sesion">
            Cerrar Sesión
          </button>
        </div>
      </div>
    );
  };

  // Componente para navegar el sistema de archivos
  const FileSystemViewer = () => {
    const [contenido, setContenido] = useState([]);
    const [contenidoArchivo, setContenidoArchivo] = useState(null);

    useEffect(() => {
      listarDirectorio(rutaActual, idParticionActual).then(setContenido);
      setContenidoArchivo(null);
    }, [rutaActual, idParticionActual]);

    const manejarNavegacion = (nombre, tipo) => {
      if (tipo === 'folder') {
        setRutaActual(rutaActual === '/' ? `/${nombre}` : `${rutaActual}/${nombre}`);
      } else if (tipo === 'file') {
        leerArchivo(`${rutaActual === '/' ? '' : rutaActual}/${nombre}`, idParticionActual)
          .then(setContenidoArchivo);
      }
    };

    const subirDirectorio = () => {
      if (rutaActual !== '/') {
        const partes = rutaActual.split('/').filter(Boolean);
        partes.pop();
        setRutaActual(partes.length ? `/${partes.join('/')}` : '/');
      }
    };

    return (
      <div className="seccion-visualizador">
        <h2 className="subtitulo">Sistema de Archivos - {idParticionActual}</h2>
        <p className="ruta-actual">Ruta: {rutaActual}</p>
        {rutaActual !== '/' && (
          <button onClick={subirDirectorio} className="boton-volver">
            Subir Directorio
          </button>
        )}
        <div className="lista-archivos">
          {contenido.map((item) => (
            <div
              key={item.name}
              className={`tarjeta-archivo ${item.type}`}
              onClick={() => manejarNavegacion(item.name, item.type)}
            >
              <span>{item.name}</span>
              <span>Tipo: {item.type}</span>
              <span>Permisos: {item.permissions}</span>
            </div>
          ))}
        </div>
        {contenidoArchivo && (
          <div className="contenido-archivo">
            <h3>Contenido del Archivo</h3>
            <pre>{contenidoArchivo}</pre>
            <button onClick={() => setContenidoArchivo(null)} className="boton-volver">
              Cerrar
            </button>
          </div>
        )}
        <div className="seccion-botones">
          <button onClick={() => setVista('partitionSelector')} className="boton-volver">
            Volver a Particiones
          </button>
          <button onClick={() => setVista('terminal')} className="boton-volver">
            Volver a Terminal
          </button>
          <button onClick={manejarCierreSesion} className="boton-cerrar-sesion">
            Cerrar Sesión
          </button>
        </div>
      </div>
    );
  };

  // Terminal (vista principal)
  const Terminal = () => (
    <div>
      <h1 className="titulo">Administrador de Sistema de Archivos EXT2</h1>
      <div className="seccion-usuario">
        {estaConectado ? (
          <div className="info-usuario">
            <span>Usuario: {usuarioActual} | Partición: {idParticionActual}</span>
            <button onClick={() => setVista('diskSelector')} className="boton-visualizador">
              Ver Sistema de Archivos
            </button>
            <button onClick={manejarCierreSesion} className="boton-cerrar-sesion">
              Cerrar Sesión
            </button>
          </div>
        ) : (
          <button onClick={() => setMostrarModal(true)} className="boton-iniciar-sesion">
            Iniciar Sesión
          </button>
        )}
      </div>
      <div className="seccion-entrada">
        <label className="etiqueta">Entrada de Comandos</label>
        <textarea
          className="textarea-comandos"
          placeholder="Ingresa tus comandos aquí..."
          value={comandos}
          onChange={(e) => setComandos(e.target.value)}
        ></textarea>
      </div>
      <div className="seccion-botones">
        <input
          type="file"
          accept=".smia"
          ref={referenciaArchivo}
          className="input-archivo"
          onChange={manejarCargaArchivo}
        />
        <button onClick={activarEntradaArchivo} className="boton-cargar">
          Cargar Script
        </button>
        <button onClick={manejarEjecucion} className="boton-ejecutar">
          Ejecutar Comandos
        </button>
      </div>
      <div className="seccion-salida">
        <label className="etiqueta">Salida de Comandos</label>
        <div className="area-salida">
          {salida || 'La salida aparecerá aquí...'}
        </div>
      </div>
    </div>
  );

  return (
    <div className="contenedor">
      {vista === 'terminal' && <Terminal />}
      {vista === 'diskSelector' && estaConectado && <DiskSelector />}
      {vista === 'partitionSelector' && estaConectado && <PartitionSelector />}
      {vista === 'fileSystem' && estaConectado && <FileSystemViewer />}
      {mostrarModal && (
        <div className="modal">
          <div className="modal-contenido">
            <h2 className="modal-titulo">Iniciar Sesión</h2>
            <form onSubmit={manejarInicioSesion}>
              <div className="campo-formulario">
                <label className="etiqueta">Usuario</label>
                <input
                  type="text"
                  value={usuario}
                  onChange={(e) => setUsuario(e.target.value)}
                  placeholder="Ingresa tu usuario"
                  className="input-login"
                />
              </div>
              <div className="campo-formulario">
                <label className="etiqueta">Contraseña</label>
                <input
                  type="password"
                  value={contrasena}
                  onChange={(e) => setContrasena(e.target.value)}
                  placeholder="Ingresa tu contraseña"
                  className="input-login"
                />
              </div>
              <div className="campo-formulario">
                <label className="etiqueta">ID de Partición</label>
                <input
                  type="text"
                  value={idParticion}
                  onChange={(e) => setIdParticion(e.target.value)}
                  placeholder="Ingresa el ID de la partición"
                  className="input-login"
                />
              </div>
              {error && <p className="mensaje-error">{error}</p>}
              <div className="modal-botones">
                <button
                  type="button"
                  onClick={() => setMostrarModal(false)}
                  className="boton-cancelar"
                >
                  Cancelar
                </button>
                <button type="submit" className="boton-ejecutar">
                  Iniciar Sesión
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}

export default App;