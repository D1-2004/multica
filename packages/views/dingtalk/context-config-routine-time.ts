/** datetime-local follows the reader's local clock; API times are UTC instants. */
export function routineLocalDateTime(iso: string): string {
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return "";
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

/** Reject invalid dates and local times normalized across a DST gap. */
export function routineRunAt(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(value)) return null;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime()) || routineLocalDateTime(date.toISOString()).slice(0, value.length) !== value) return null;
  return date.toISOString();
}
