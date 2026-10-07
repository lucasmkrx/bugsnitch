// SPDX-License-Identifier: MPL-2.0
import type { HistoryAnchor, ReferencePage } from '@bugsnitch/shared';

export function HistoryPicker({
  anchor,
  references,
  disabled,
  onSelect,
}: {
  anchor: HistoryAnchor;
  references: ReferencePage;
  disabled: boolean;
  onSelect: (name: string | null) => void;
}) {
  return (
    <div className="history-picker">
      <label htmlFor="history-entry">History entry point</label>
      <select
        id="history-entry"
        disabled={disabled}
        value={
          anchor.kind === 'ref'
            ? anchor.ref
            : anchor.kind === 'commit'
              ? 'commit'
              : 'head'
        }
        onChange={(event) => {
          if (event.target.value !== 'commit')
            onSelect(event.target.value === 'head' ? null : event.target.value);
        }}
      >
        <option value="head">Current checkout (HEAD)</option>
        {anchor.kind === 'commit' && (
          <option value="commit">{anchor.label}</option>
        )}
        {(
          [
            ['branch', 'Branches'],
            ['remote', 'Remote branches (local refs)'],
            ['tag', 'Tags'],
          ] as const
        ).map(([kind, label]) => {
          const options = references.references.filter(
            (ref) => ref.kind === kind,
          );
          return options.length ? (
            <optgroup key={kind} label={label}>
              {options.map((ref) => (
                <option key={ref.name} value={ref.name}>
                  {ref.label}
                </option>
              ))}
            </optgroup>
          ) : null;
        })}
      </select>
      <p className="hint">Browse local history without checkout or fetch.</p>
      {references.truncated && (
        <p className="hint" role="status">
          Showing the first 2,000 local refs. Other entry points are outside
          this bounded list.
        </p>
      )}
    </div>
  );
}
