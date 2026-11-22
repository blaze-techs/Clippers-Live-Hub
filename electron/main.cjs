const { app, BrowserWindow, dialog, shell } = require('electron');
const path = require('node:path');
const { spawn } = require('node:child_process');
function createWindow() {
  const win = new BrowserWindow({
    width: 1500, height: 940, minWidth: 980, minHeight: 680,
    backgroundColor: '#080b12',
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true }
  });
  win.loadFile(path.join(__dirname, '..', 'dist', 'index.html'));
  win.webContents.setWindowOpenHandler(({ url }) => { shell.openExternal(url); return { action: 'deny' }; });
}
app.whenReady().then(() => { createWindow(); app.on('activate', () => { if (BrowserWindow.getAllWindows().length === 0) createWindow(); }); });
app.on('window-all-closed', () => { if (process.platform !== 'darwin') app.quit(); });