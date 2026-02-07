import path from "node:path";

const root = import.meta.dir ? path.resolve(import.meta.dir, "..") : process.cwd();
const outdir = path.join(root, "dist");
const minify = process.env.NODE_ENV === "production";

const result = await Bun.build({
  entrypoints: [path.join(root, "src", "client.tsx")],
  outfile: path.join(outdir, "client.js"),
  target: "browser",
  format: "esm",
  splitting: false,
  sourcemap: "linked",
  minify,
});

if (!result.success) {
  console.error("bun build failed");
  for (const log of result.logs) {
    console.error(log);
  }
  process.exit(1);
}

console.log(`built ${result.outputs.length} output(s) into ${outdir}`);
