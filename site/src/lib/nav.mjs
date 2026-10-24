// The sidebar: a page not listed here is reachable only by its address.
export const sidebar = [
	{ label: 'Start here', items: [{ label: 'Getting started', slug: 'start' }] },
	{
		label: 'Gates for tests and code',
		items: [
			{ label: 'mutate', slug: 'mutate' },
			{ label: 'testquality', slug: 'testquality' },
			{ label: 'ctxlife', slug: 'ctxlife' },
			{ label: 'refs', slug: 'refs' },
			{ label: 'vulngate', slug: 'vulngate' },
			{ label: 'taskgate', slug: 'taskgate' },
		],
	},
	{
		label: 'Dependencies and releases',
		items: [
			{ label: 'versions', slug: 'versions' },
			{ label: 'versions.yaml reference', slug: 'versions/reference' },
			{ label: 'sfw', slug: 'sfw' },
			{ label: 'Provenance and SBOMs', slug: 'supply-chain' },
		],
	},
];
