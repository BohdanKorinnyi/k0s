// A handful of line icons, drawn inline so there is no icon package to load.
// Each is a 16×16 path set, stroked with the current text colour.

const paths = {
  restart: 'M13 3v3.5H9.5M3 13V9.5h3.5M12.3 6.5A4.8 4.8 0 0 0 4 5.4M3.7 9.5A4.8 4.8 0 0 0 12 10.6',
  pull: 'M8 2.5v8M4.8 7.5 8 10.7l3.2-3.2M3 13.5h10',
  logs: 'M3 4h10M3 8h10M3 12h6',
  shell: 'M3 4.5 6.5 8 3 11.5M8.5 11.5H13',
  more: 'M3.5 8h.01M8 8h.01M12.5 8h.01',
  start: 'M5 3.5v9l7-4.5z',
  stop: 'M4.5 4.5h7v7h-7z',
  lock: 'M4.5 7.5h7v5.5h-7zM6 7.5V5.6a2 2 0 0 1 4 0v1.9',
  back: 'M9.5 3.5 5 8l4.5 4.5',
  close: 'M4 4l8 8M12 4l-8 8',
  expand: 'M3 6V3h3M13 6V3h-3M3 10v3h3M13 10v3h-3',
  shrink: 'M6 3v3H3M10 3v3h3M6 13v-3H3M10 13v-3h3',
  download: 'M8 2.5v7.5M5 7l3 3 3-3M3 13.5h10',
  eye: 'M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8zM8 9.8a1.8 1.8 0 1 0 0-3.6 1.8 1.8 0 0 0 0 3.6z',
  copy: 'M5.5 5.5v-2h7v7h-2M3.5 5.5h7v7h-7z',
  refresh: 'M13 3v3.5H9.5M12.4 6.5a4.8 4.8 0 1 0 .3 3.5',
  plus: 'M8 3.5v9M3.5 8h9',
  check: 'M3.5 8.5 6.5 11.5 12.5 4.5',
} as const

export type IconName = keyof typeof paths

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  )
}
