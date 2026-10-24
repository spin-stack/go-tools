// @ts-check
import starlight from '@astrojs/starlight';
import { defineConfig } from 'astro/config';
import { sidebar } from './src/lib/nav.mjs';

// A GitHub project page: https://spin-stack.github.io/go-tools/.
const site = process.env.SITE_URL || 'https://spin-stack.github.io';
const base = process.env.BASE_PATH || '/go-tools';

export default defineConfig({
	site,
	base,
	trailingSlash: 'always',
	// The examples are read from the repository's testdata, outside the site (src/lib/example.ts).
	vite: { server: { fs: { allow: ['..'] } } },
	integrations: [
		starlight({
			title: 'go-tools',
			description:
				'Go tools that hold a repository to what it claims: tests that notice a change, pins written once, releases that are attested.',
			// spin-stack/brand's mark and palette, as the Spinbox site has them (spin-stack/website).
			logo: { dark: './src/assets/logo.png', light: './src/assets/logo-light.png', alt: '' },
			favicon: '/favicon.png',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/spin-stack/go-tools' }],
			customCss: ['./src/styles/brand-tokens.css', './src/styles/brand-starlight.css', './src/styles/custom.css'],
			editLink: { baseUrl: 'https://github.com/spin-stack/go-tools/edit/main/site/' },
			lastUpdated: true,
			head: [{ tag: 'link', attrs: { rel: 'apple-touch-icon', href: `${base.replace(/\/$/, '')}/apple-touch-icon.png` } }],
			sidebar,
		}),
	],
});
