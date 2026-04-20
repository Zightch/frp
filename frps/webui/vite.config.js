import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
var __dirname = dirname(fileURLToPath(import.meta.url));
export default defineConfig({
    plugins: [vue()],
    resolve: {
        alias: {
            '@': resolve(__dirname, 'src')
        }
    },
    build: {
        outDir: 'dist',
        emptyOutDir: true
    },
    server: {
        port: 5173,
        proxy: {
            '/api': {
                target: 'http://localhost:7000',
                changeOrigin: true
            }
        }
    }
});
