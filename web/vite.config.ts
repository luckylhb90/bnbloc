import { defineConfig, type Plugin } from "vite";
import { fileURLToPath, URL } from "node:url";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { execSync } from "node:child_process";
import { readFileSync } from "node:fs";

// Build identity shown (quietly) in the UI. The product version is the desktop
// app's (one version for the release); BNBLOC_VERSION / CI_COMMIT_SHA override
// it, e.g. in image builds where .git is not in the build context.
function buildVersion(): { version: string; commit: string } {
  let version = process.env.BNBLOC_VERSION?.trim() || "";
  if (!version) {
    try {
      version = JSON.parse(readFileSync(new URL("../apps/desktop/package.json", import.meta.url), "utf8")).version ?? "";
    } catch {
      version = "";
    }
  }
  let commit = process.env.CI_COMMIT_SHA?.trim() || "";
  if (!commit) {
    try {
      commit = execSync("git rev-parse --short HEAD", { stdio: ["ignore", "pipe", "ignore"] }).toString().trim();
    } catch {
      commit = "";
    }
  }
  return { version: version || "dev", commit };
}
const build = buildVersion();

// build.json beside index.html carries the same identity for the gateway that
// embeds this bundle (services/gateway/webui Build), so the deployment view
// reports the web commit next to the service commits.
const buildManifest: Plugin = {
  name: "bnbloc-build-manifest",
  generateBundle() {
    this.emitFile({ type: "asset", fileName: "build.json", source: JSON.stringify(build) + "\n" });
  },
};

// Builds the bnbloc web client into ../services/gateway/webui/static so the
// gateway embeds it via //go:embed. Base is relative so the SPA works when
// served from the gateway root.
export default defineConfig({
  plugins: [react(), tailwindcss(), buildManifest],
  define: {
    __BNBLOC_VERSION__: JSON.stringify(build.version),
    __BNBLOC_COMMIT__: JSON.stringify(build.commit),
  },
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  base: "./",
  build: {
    outDir: "../services/gateway/webui/static",
    emptyOutDir: true,
    // Single-file-ish output keeps the embed simple; hashed asset names are fine.
    rollupOptions: {
      output: {
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
  server: {
    // Dev proxy so `pnpm dev` talks to a locally running gateway on :7090.
    proxy: {
      "/v1": "http://localhost:7090",
      "/api": "http://localhost:7090",
    },
  },
});
