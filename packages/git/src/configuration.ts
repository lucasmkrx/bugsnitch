// SPDX-License-Identifier: MPL-2.0
import { GitError } from './errors';

export function filterOverrides(keys: string): string[] {
  const drivers = new Set<string>();
  for (const key of keys.split('\0').filter(Boolean)) {
    const match = /^(filter\..+)\.(?:clean|smudge|process|required)$/i.exec(
      key,
    );
    if (!match || key.includes('='))
      throw new GitError(
        'failed',
        'Git content-filter configuration is unsupported. Check the filter names in your Git configuration.',
      );
    drivers.add(match[1]!);
  }
  return [...drivers].flatMap((driver) => [
    '-c',
    `${driver}.clean=`,
    '-c',
    `${driver}.smudge=`,
    '-c',
    `${driver}.process=`,
    '-c',
    `${driver}.required=false`,
  ]);
}
