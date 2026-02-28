const fs = require('fs');
const path = require('path');

const STATE_FILE = path.join(__dirname, '.auth-state.json');

function saveState(key, value) {
  let state = {};
  try { state = JSON.parse(fs.readFileSync(STATE_FILE, 'utf-8')); } catch {}
  state[key] = value;
  fs.writeFileSync(STATE_FILE, JSON.stringify(state));
}

function getState(key) {
  try {
    const state = JSON.parse(fs.readFileSync(STATE_FILE, 'utf-8'));
    return state[key];
  } catch {
    return null;
  }
}

function clearState() {
  try { fs.unlinkSync(STATE_FILE); } catch {}
}

module.exports = { saveState, getState, clearState };
