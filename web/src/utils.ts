import { useEffect, useState } from "react";

export function formatDateTime(ts?: string): string {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return "-";
  }
}

export function formatTime(ts?: string | null): string {
  if (!ts) return "-";
  try {
    return new Date(ts).toLocaleTimeString();
  } catch {
    return "-";
  }
}

export function normalizeStatus(status: string): string {
  return String(status || "idle")
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9_-]+/g, "-");
}

export function parseJSONSafe(raw: string): { ok: true; value: unknown; error: "" } | { ok: false; value: null; error: string } {
  try {
    return { ok: true, value: JSON.parse(raw), error: "" };
  } catch (err) {
    return { ok: false, value: null, error: err instanceof Error ? err.message : String(err) };
  }
}

export function toJSON(value: unknown): string {
  if (value === null || value === undefined) return "";
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function isPrimitive(value: unknown): value is string | number | boolean | null {
  return value === null || ["string", "number", "boolean"].includes(typeof value);
}

export function usePrefersDark(): boolean {
  const getValue = () =>
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches;

  const [dark, setDark] = useState(getValue);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return undefined;
    }
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (event: MediaQueryListEvent) => setDark(event.matches);
    if (typeof query.addEventListener === "function") {
      query.addEventListener("change", onChange);
      return () => query.removeEventListener("change", onChange);
    }
    query.addListener(onChange);
    return () => query.removeListener(onChange);
  }, []);

  return dark;
}

export function xmlTheme(darkMode: boolean): Record<string, string> {
  if (darkMode) {
    return {
      tagColor: "#87b5f2",
      textColor: "#e4ebe7",
      attributeKeyColor: "#ff9460",
      attributeValueColor: "#8ce3c0",
      separatorColor: "#98aaa4",
      commentColor: "#93a6a0",
      cdataColor: "#8ce3c0",
      fontFamily: "IBM Plex Mono, monospace",
      lineNumberBackground: "transparent",
      lineNumberColor: "#98aaa4",
    };
  }
  return {
    tagColor: "#3f689c",
    textColor: "#1e1c16",
    attributeKeyColor: "#d2612c",
    attributeValueColor: "#1f7a58",
    separatorColor: "#6c6658",
    commentColor: "#6c6658",
    cdataColor: "#1f7a58",
    fontFamily: "IBM Plex Mono, monospace",
    lineNumberBackground: "transparent",
    lineNumberColor: "#6c6658",
  };
}
