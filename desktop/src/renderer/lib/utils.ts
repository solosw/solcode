import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** Merge conditional class names with Tailwind conflict resolution. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs))
}

/** Render an absolute path relative to the session's working directory. */
export function relativePath(path: string, cwd: string | null): string {
  if (cwd === null || cwd === '') return path
  const normalisedCwd = cwd.replace(/[\\/]+$/u, '')
  if (path === normalisedCwd) return '.'
  for (const separator of ['\\', '/']) {
    const prefix = `${normalisedCwd}${separator}`
    if (path.startsWith(prefix)) return path.slice(prefix.length)
  }
  return path
}

/** Final path segment, used as the compact label for a changed file. */
export function baseName(path: string): string {
  const parts = path.split(/[\\/]/u)
  return parts[parts.length - 1] ?? path
}

/** Compact token count, e.g. 128400 -> "128k". */
export function formatTokens(value: number): string {
  if (value < 1000) return String(value)
  if (value < 1_000_000) return `${Math.round(value / 100) / 10}k`
  return `${Math.round(value / 100_000) / 10}M`
}
