// SPDX-License-Identifier: MPL-2.0
export type GitErrorCode =
  | 'missingGit'
  | 'notRepository'
  | 'emptyRepository'
  | 'untracked'
  | 'binary'
  | 'invalidInput'
  | 'cancelled'
  | 'timeout'
  | 'tooLarge'
  | 'failed';

export class GitError extends Error {
  constructor(
    public readonly code: GitErrorCode,
    message: string,
  ) {
    super(message);
    this.name = 'GitError';
  }
}
