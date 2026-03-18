import path from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      {
        find: 'react-webrtp/deskview',
        replacement: path.resolve(__dirname, '../src/deskview-entry.ts'),
      },
      {
        find: 'react-webrtp',
        replacement: path.resolve(__dirname, '../src/index.ts'),
      },
    ],
  },
  server: {
    port: 5173,
  },
});
