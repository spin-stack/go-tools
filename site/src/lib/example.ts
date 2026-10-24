// mutate's example, as cmd/mutate's TestTheExampleTheSiteShows runs it: the code, the tests, and
// what mutate printed over them. The site shows these files and never a copy of them.
const files = import.meta.glob<string>('../../../cmd/mutate/testdata/example/**/*', {
	query: '?raw',
	import: 'default',
	eager: true,
});

/** The example's file at path (relative to the example), as the test has it; a path it does not have fails the build. */
export function example(path: string): string {
	const text = files[`../../../cmd/mutate/testdata/example/${path}`];
	if (text === undefined) throw new Error(`no ${path} in cmd/mutate/testdata/example`);
	return text.replace(/\n$/, '');
}
