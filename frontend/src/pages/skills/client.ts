// Writing skills to the runtime's own directory.
//
// The inventory is scanned from disk, so these commands only put a folder where
// the runtime looks and then re-scan: "installed" always means the file exists.

import { invokeHost, isTauriHost } from '../../host/tauri'

export function hasSkillHost(): boolean {
  return isTauriHost()
}

export async function createSkill(name: string, description: string, body: string): Promise<string> {
  return invokeHost<string>('skill_create', { name, description, body })
}

export async function importSkill(path: string): Promise<string> {
  return invokeHost<string>('skill_import', { path })
}

export async function removeSkill(path: string): Promise<void> {
  await invokeHost('skill_remove', { path })
}
