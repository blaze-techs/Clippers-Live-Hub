const { app, BrowserWindow, dialog } = require('electron');
const path = require('node:path');
const { spawn } = require('node:child_process');
const http = require('node:http');

let backend;
const PORT = 18765;
const isDev = !app.isPackaged;

function runtimePath(...parts) {
  return isDev
    ? path.join(__dirname, '..', 'runtime', ...parts)
    : path.join(process.resourcesPath, 'runtime', ...parts);
}
function waitForBackend(timeoutMs = 20000) {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const poll = () => {
      const req = http.get(`http://127.0.0.1:${PORT}/api/status`, res => {
        res.resume();
        if (res.statusCode === 200) return resolve();
        retry();
      });
      req.on('error', retry);
      req.setTimeout(1000, () => req.destroy());
      function retry() {
        if (Date.now() - started > timeoutMs) return reject(new Error('Clippers engine did not start in time.'));
        setTimeout(poll, 350);
      }
    };
    poll();
  });
}
function startBackend() {
  const exe = runtimePath('ClippersLiveHub.exe');
  backend = spawn(exe, ['-host', '127.0.0.1', '-port', String(PORT), '-open=false'], {
    windowsHide: true,
    stdio: 'ignore',
    env: { ...process.env, CLIPPERS_HOME: app.getPath('userData') }
  });
  backend.on('error', err => dialog.showErrorBox('Clippers Live Hub engine error', err.message));
  backend.on('exit', code => {
    if (!app.isQuitting && code && code !== 0) dialog.showErrorBox('Clippers Live Hub stopped', `The video engine exited with code ${code}.`);
  });
}
async function createWindow() {
  const win = new BrowserWindow({
    width: 1500, height: 940, minWidth: 980, minHeight: 680,
    backgroundColor: '#080b12',
    autoHideMenuBar: true,
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true }
  });
  try {
    await waitForBackend();
    await win.loadURL(`http://127.0.0.1:${PORT}/`);
  } catch (error) {
    dialog.showErrorBox('Clippers Live Hub could not start', error.message + '\n\nPlease reinstall the application if the problem persists.');
    app.quit();
  }
}
app.whenReady().then(async () => {
  startBackend();
  await createWindow();
  app.on('activate', () => { if (BrowserWindow.getAllWindows().length === 0) createWindow(); });
});
app.on('before-quit', () => {
  app.isQuitting = true;
  if (backend && !backend.killed) backend.kill();
});
app.on('window-all-closed', () => { if (process.platform !== 'darwin') app.quit(); });
