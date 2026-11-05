import plugin from 'tailwindcss/plugin'

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        ['low-latency']: 'var(--app-latency-low)',
        ['medium-latency']: 'var(--app-latency-medium)',
        ['high-latency']: 'var(--app-latency-high)',
      },
    },
  },
  plugins: [
    plugin(({ addUtilities }) => {
      addUtilities({
        '.scrollbar-hidden': {
          'scrollbar-width': 'none!important',
        },
        '.scrollbar-thin': {
          'scrollbar-width': 'thin',
        },
      })
    }),
  ],
}
