// src/Login.js
import { useState } from 'react';
import './App.css'; // Reutilizar estilos existentes

function Login({ onLogin }) {
  const [usuario, setUsuario] = useState('');
  const [contrasena, setContrasena] = useState('');
  const [idParticion, setIdParticion] = useState('');
  const [error, setError] = useState('');
  
  // Configuración dinámica del backend
  const BACKEND_URL = window.location.hostname.includes('3.145.6.97') || 
                     window.location.hostname === 'ec2-3-145-6-97.us-east-2.compute.amazonaws.com' 
                     ? 'http://3.145.6.97:8080' 
                     : 'http://localhost:8080';

  const manejarInicioSesion = async (e) => {
    e.preventDefault();
    setError('');

    // Validar campos
    if (!usuario || !contrasena || !idParticion) {
      setError('Por favor, completa todos los campos');
      return;
    }

    // Construir comando de login
    const comandoLogin = `login -user="${usuario}" -pass="${contrasena}" -id="${idParticion}"`;

    try {
      const respuesta = await fetch(`${BACKEND_URL}/execute`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ comandos: comandoLogin }),
      });
      const resultado = await respuesta.json();
      const salida = resultado.salida || '';

      if (salida.includes('Sesión iniciada para')) {
        onLogin(usuario, idParticion); // Pasar usuario e ID de partición al componente padre
      } else {
        setError(salida || 'Error al iniciar sesión');
      }
    } catch (error) {
      setError(`Error: ${error.message}`);
    }
  };

  return (
    <div className="contenedor">
      <h1>Iniciar Sesión</h1>
      <div className="seccion-entrada">
        <form onSubmit={manejarInicioSesion}>
          <div className="campo-formulario">
            <label>Usuario</label>
            <input
              type="text"
              value={usuario}
              onChange={(e) => setUsuario(e.target.value)}
              placeholder="Ingresa tu usuario"
              className="input-login"
            />
          </div>
          <div className="campo-formulario">
            <label>Contraseña</label>
            <input
              type="password"
              value={contrasena}
              onChange={(e) => setContrasena(e.target.value)}
              placeholder="Ingresa tu contraseña"
              className="input-login"
            />
          </div>
          <div className="campo-formulario">
            <label>ID de Partición</label>
            <input
              type="text"
              value={idParticion}
              onChange={(e) => setIdParticion(e.target.value)}
              placeholder="Ingresa el ID de la partición"
              className="input-login"
            />
          </div>
          {error && <div className="mensaje-error">{error}</div>}
          <div className="seccion-botones">
            <button type="submit" className="boton-ejecutar">
              Iniciar Sesión
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export default Login;