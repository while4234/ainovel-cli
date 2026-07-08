import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

const configDir = path.dirname(fileURLToPath(import.meta.url));
const staticDir = path.resolve(configDir, '../static');

function normalizeStaticLineEndings() {
  return {
    name: 'normalize-static-line-endings',
    apply: 'build',
    async closeBundle() {
      await normalizeTextOutputs(staticDir);
    }
  };
}

async function normalizeTextOutputs(directory) {
  let entries;
  try {
    entries = await fs.readdir(directory, { withFileTypes: true });
  } catch (error) {
    if (error && error.code === 'ENOENT') {
      return;
    }
    throw error;
  }

  await Promise.all(entries.map(async (entry) => {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      await normalizeTextOutputs(fullPath);
      return;
    }
    if (!entry.isFile() || !/\.(html|css|js)$/.test(entry.name)) {
      return;
    }
    const text = await fs.readFile(fullPath, 'utf8');
    const normalized = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n');
    if (normalized !== text) {
      await fs.writeFile(fullPath, normalized, 'utf8');
    }
  }));
}

export default defineConfig({
  plugins: [react(), normalizeStaticLineEndings()],
  build: {
    outDir: '../static',
    emptyOutDir: true,
    sourcemap: false
  },
  test: {
    environment: 'node'
  }
});
