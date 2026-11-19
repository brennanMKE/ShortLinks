// Keyboard model and item type for RowMenu (#0155).

export interface MenuItem {
  label: string;
  onselect?: () => void;
  /** Renders a link instead of a button (e.g. "Open destination"). */
  href?: string;
  /** Destructive items are tinted, never a red button (principle 6). */
  danger?: boolean;
  disabled?: boolean;
}

/** Next focused item index for a key pressed inside an open menu; null = not handled. */
export function nextMenuIndex(key: string, index: number, count: number): number | null {
  if (count <= 0) return null;
  switch (key) {
    case 'ArrowDown':
      return (index + 1) % count;
    case 'ArrowUp':
      return index <= 0 ? count - 1 : index - 1;
    case 'Home':
      return 0;
    case 'End':
      return count - 1;
    default:
      return null;
  }
}
