// SPDX-License-Identifier: MPL-2.0
import type { GraphNode } from '@bugsnitch/graph';

const spacing = 18;
const x = (lane: number) => 12 + lane * spacing;

export function GraphLanes({
  node,
  laneCount,
  boundary,
}: {
  node: GraphNode;
  laneCount: number;
  boundary: boolean;
}) {
  const width = Math.max(30, laneCount * spacing + 6);
  return (
    <span className="graph-lanes" style={{ width }} aria-hidden="true">
      <svg
        className="graph-lines"
        viewBox={`0 0 ${width} 100`}
        preserveAspectRatio="none"
        focusable="false"
      >
        {node.segments.map((segment, index) => {
          const start = segment.from === 'top' ? 0 : 50;
          const end = segment.to === 'node' ? 50 : 100;
          return (
            <path
              key={index}
              className={`graph-color-${segment.colorLane % 5}`}
              d={`M ${x(segment.fromLane)} ${start} C ${x(segment.fromLane)} ${(start + end) / 2}, ${x(segment.toLane)} ${(start + end) / 2}, ${x(segment.toLane)} ${end}`}
              fill="none"
              strokeWidth="2"
              vectorEffect="non-scaling-stroke"
              strokeDasharray={
                boundary && segment.to === 'bottom' ? '4 3' : undefined
              }
            />
          );
        })}
      </svg>
      <svg
        className={`graph-node graph-color-${node.lane % 5}`}
        style={{ left: x(node.lane) - 6 }}
        width="12"
        height="12"
        viewBox="0 0 12 12"
        focusable="false"
      >
        <circle cx="6" cy="6" r={node.parents.length > 1 ? 5 : 4} />
      </svg>
    </span>
  );
}
