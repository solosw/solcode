/** Ambient declaration for the preload bridge. */
import type { DesktopBridge } from '@shared/protocol'

declare global {
  interface Window {
    readonly solcode: DesktopBridge
  }
}

export {}
