// SPDX-License-Identifier: MPL-2.0
import type { Commit } from '@bugsnitch/shared';

export interface GraphNode {
  hash: string;
  parents: string[];
  refs: string[];
  row: number;
  lane: number;
  segments: GraphSegment[];
}
export interface GraphSegment {
  fromLane: number;
  toLane: number;
  from: 'top' | 'node';
  to: 'node' | 'bottom';
  colorLane: number;
}
export interface GraphEdge {
  from: string;
  to: string;
  outsidePage: boolean;
}
export interface CommitGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
  laneCount: number;
  boundaries: { hash: string; lane: number }[];
}

/**
 * Lay out a child-before-parent history prefix. Reserve lanes for unseen parents
 * and never compact live lanes: appending an older page preserves every prior
 * node and segment. Multiple children targeting one parent share its lane.
 */
export function createCommitGraph(commits: readonly Commit[]): CommitGraph {
  const visible = new Set(commits.map((commit) => commit.hash));
  const lanes: (string | null)[] = [];
  let laneCount = 0;
  const allocate = (hash: string): number => {
    const existing = lanes.indexOf(hash);
    if (existing !== -1) return existing;
    const empty = lanes.indexOf(null);
    const lane = empty === -1 ? lanes.length : empty;
    lanes[lane] = hash;
    laneCount = Math.max(laneCount, lane + 1);
    return lane;
  };
  const nodes = commits.map((commit, row): GraphNode => {
    const incoming = [...lanes];
    const lane = allocate(commit.hash);
    const segments: GraphSegment[] = [];
    incoming.forEach((hash, fromLane) => {
      if (hash === null) return;
      segments.push({
        fromLane,
        toLane: hash === commit.hash ? lane : fromLane,
        from: 'top',
        to: hash === commit.hash ? 'node' : 'bottom',
        colorLane: fromLane,
      });
    });
    lanes[lane] = null;
    // The first parent continues this lane when it has no lane already.
    for (const parent of commit.parents) {
      if (lanes.indexOf(parent) === -1 && lanes[lane] === null)
        lanes[lane] = parent;
      const toLane = allocate(parent);
      segments.push({
        fromLane: lane,
        toLane,
        from: 'node',
        to: 'bottom',
        colorLane: toLane,
      });
    }
    return {
      hash: commit.hash,
      parents: [...commit.parents],
      refs: [...commit.refs],
      row,
      lane,
      segments,
    };
  });
  return {
    nodes,
    edges: commits.flatMap((commit) =>
      commit.parents.map((parent) => ({
        from: commit.hash,
        to: parent,
        outsidePage: !visible.has(parent),
      })),
    ),
    laneCount,
    boundaries: lanes.flatMap((hash, lane) =>
      hash === null ? [] : [{ hash, lane }],
    ),
  };
}
