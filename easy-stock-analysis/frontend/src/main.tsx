import React from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { initializeFontSettings } from './lib/font-settings';
import { installRuntimeLogging } from './lib/runtime-log';
import './styles.css';
import './theme.css';

installRuntimeLogging();
initializeFontSettings();

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
