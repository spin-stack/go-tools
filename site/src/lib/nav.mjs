// The sidebar: a page not listed here is reachable only by its address.
export const sidebar = [
	{
		label: 'Start here',
		items: [
			{ label: 'Getting started', slug: 'start' },
			{ label: 'GitHub Actions', slug: 'github-actions' },
			{ label: 'The common tasks', slug: 'tasks' },
		],
	},
	{
		label: 'Gates for tests and code',
		items: [
			{ label: 'mutate', slug: 'mutate' },
			{ label: 'coverfloor', slug: 'coverfloor' },
			{ label: 'testquality', slug: 'testquality' },
			{ label: 'ctxlife', slug: 'ctxlife' },
			{ label: 'refs', slug: 'refs' },
			{ label: 'vulngate', slug: 'vulngate' },
			{ label: 'taskgate', slug: 'taskgate' },
			{ label: 'testidentity', slug: 'testidentity' },
			{ label: 'deadgate', slug: 'deadgate' },
			{ label: 'linkweight', slug: 'linkweight' },
			{ label: 'proven', slug: 'proven' },
		],
	},
	{
		label: 'Dependencies and releases',
		items: [
			{ label: 'versions', slug: 'versions' },
			{ label: 'versions.yaml reference', slug: 'versions/reference' },
			{ label: 'release', slug: 'release' },
			{ label: 'consumers', slug: 'consumers' },
			{ label: 'sfw', slug: 'sfw' },
			{ label: 'Provenance and SBOMs', slug: 'supply-chain' },
			{ label: 'exposure', slug: 'exposure' },
			{ label: 'planguard', slug: 'planguard' },
		],
	},
	{ label: 'Doing less work', items: [{ label: 'gencache', slug: 'gencache' }] },
	{ label: 'Seeing where CI spends', items: [{ label: 'ghatrace', slug: 'ghatrace' }] },
];
