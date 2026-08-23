import type { SearchMode } from '../api/vault'

const options: Array<{ value: SearchMode; label: string }> = [
  { value: 'hybrid', label: 'Hybrid（推荐）' },
  { value: 'vector', label: 'Vector' },
  { value: 'bm25', label: 'BM25' },
]

export function SearchModeField({
  value,
  onChange,
}: {
  value: SearchMode
  onChange(value: SearchMode): void
}): React.ReactElement {
  return (
    <label className="mode-field">
      <span>检索模式</span>
      <select value={value} onChange={(event) => onChange(event.target.value as SearchMode)}>
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  )
}
