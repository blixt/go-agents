import { useSyncExternalStore } from "react";
import { runtimeStore } from "../runtimeStore";
import type { RuntimeState, RuntimeStatus } from "../types";

export function useRuntimeState(): { state: RuntimeState; status: RuntimeStatus; refresh: () => Promise<void> } {
  const snapshot = useSyncExternalStore(runtimeStore.subscribe, runtimeStore.getSnapshot, runtimeStore.getSnapshot);
  return {
    state: snapshot.state,
    status: snapshot.status,
    refresh: runtimeStore.refresh,
  };
}
