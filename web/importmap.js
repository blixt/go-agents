(map => {
  const mapUrl = document.currentScript.src;
  const resolve = imports => Object.fromEntries(Object.entries(imports ).map(([k, v]) => [k, new URL(v, mapUrl).href]));
  document.head.appendChild(Object.assign(document.createElement("script"), {
    type: "importmap",
    innerHTML: JSON.stringify({
      imports: resolve(map.imports),
      scopes: Object.fromEntries(Object.entries(map.scopes).map(([k, v]) => [new URL(k, mapUrl).href, resolve(v)]))
    })
  }));
})
({
  "imports": {
    "go-agents-ui": "./app.js"
  },
  "scopes": {
    "./": {
      "dompurify": "https://ga.jspm.io/npm:dompurify@3.3.1/dist/purify.es.mjs",
      "marked": "https://ga.jspm.io/npm:marked@17.0.1/lib/marked.esm.js",
      "react": "https://ga.jspm.io/npm:react@19.2.4/dev.index.js",
      "react-dom/client": "https://ga.jspm.io/npm:react-dom@19.2.4/dev.client.js"
    },
    "https://ga.jspm.io/npm:react-dom@19.2.4/": {
      "react": "https://ga.jspm.io/npm:react@19.2.4/dev.index.js",
      "react-dom": "https://ga.jspm.io/npm:react-dom@19.2.4/dev.index.js",
      "scheduler": "https://ga.jspm.io/npm:scheduler@0.27.0/dev.index.js"
    }
  }
});
