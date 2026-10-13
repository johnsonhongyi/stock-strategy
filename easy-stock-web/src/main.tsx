import React from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { AuthGate } from './components/AuthGate';
import { installRuntimeLogging } from './lib/runtime-log';
import './styles.css';
import './theme.css';

installRuntimeLogging();

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AuthGate><App /></AuthGate>
  </React.StrictMode>,
);
