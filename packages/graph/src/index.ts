// SPDX-License-Identifier: MPL-2.0
import type { Commit } from '@bugsnitch/shared';

export interface GraphNode {
  hash: string;
  parents: string[];
  refs: string[];
  row: number;
}
export interface GraphEdge {
  from: string;
  to: string;
  outsidePage: boolean;
}
export interface CommitGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

/** A bounded graph model. Lane layout and drawing are deliberately separate future work. */
export function createCommitGraph(commits: readonly Commit[]): CommitGraph {
  const visible = new Set(commits.map((commit) => commit.hash));
  return {
    nodes: commits.map((commit, row) => ({
      hash: commit.hash,
      parents: [...commit.parents],
      refs: [...commit.refs],
      row,
    })),
    edges: commits.flatMap((commit) =>
      commit.parents.map((parent) => ({
        from: commit.hash,
        to: parent,
        outsidePage: !visible.has(parent),
      })),
    ),
  };
}
