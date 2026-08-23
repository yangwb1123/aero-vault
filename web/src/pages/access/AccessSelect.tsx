export interface SelectOption<T extends string> {
  value: T
  label: string
}

export function AccessSelect<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: T
  options: Array<SelectOption<T>>
  onChange(value: T): void
}): React.ReactElement {
  return (
    <label className="access-field">
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value as T)}>
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  )
}
