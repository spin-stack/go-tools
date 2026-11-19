package planguard

import (
	"strings"
	"testing"
)

// planOf is a plan's JSON with one resource change for each of changes, "<type>.<name> <action>,...".
func planOf(changes ...string) []byte {
	var rcs []string
	for _, c := range changes {
		addr, acts, _ := strings.Cut(c, " ")
		typ, _, _ := strings.Cut(addr, ".")
		quoted := `"` + strings.ReplaceAll(acts, ",", `","`) + `"`
		rcs = append(rcs, `{"address":"module.spin.`+addr+`","type":"`+typ+`","mode":"managed","change":{"actions":[`+quoted+`]}}`)
	}
	return []byte(`{"format_version":"1.2","resource_changes":[` + strings.Join(rcs, ",") + `]}`)
}

// A plan is nothing with no change to a resource, applied with creates and in-place updates alone,
// and a person's with anything taken away - a delete, a replacement, a forget, an action not known -
// unless it replaces a type named as made to be replaced; never a delete alone.
func TestAPlanIsAppliedUnreadOnlyWhenNothingIsTakenAway(t *testing.T) {
	replaceable := []string{"aws_launch_template"}
	for name, tc := range map[string]struct {
		plan    []byte
		verdict Verdict
		held    []string
		changes int
	}{
		"no changes":             {planOf("aws_s3_bucket.logs no-op", "aws_ami.image read"), Nothing, nil, 0},
		"none at all":            {[]byte(`{"format_version":"1.2"}`), Nothing, nil, 0},
		"creates and updates":    {planOf("aws_launch_template.cp update", "aws_ssm_parameter.pcrs create", "aws_kms_key.cp update"), Apply, nil, 3},
		"a delete":               {planOf("aws_launch_template.cp update", "aws_s3_bucket.logs delete"), Person, []string{"module.spin.aws_s3_bucket.logs: delete"}, 2},
		"a replacement":          {planOf("aws_db_instance.state delete,create"), Person, []string{"module.spin.aws_db_instance.state: delete, create"}, 1},
		"a create-first replace": {planOf("aws_kms_key.cp create,delete"), Person, []string{"module.spin.aws_kms_key.cp: create, delete"}, 1},
		"a forget":               {planOf("aws_route53_zone.z forget"), Person, []string{"module.spin.aws_route53_zone.z: forget"}, 1},
		"an action not known":    {planOf("aws_iam_role.r adopt"), Person, []string{"module.spin.aws_iam_role.r: adopt"}, 1},
		"a replaceable replaced": {planOf("aws_launch_template.cp delete,create", "aws_launch_template.px create,delete"), Apply, nil, 2},
		"a replaceable deleted":  {planOf("aws_launch_template.cp delete"), Person, []string{"module.spin.aws_launch_template.cp: delete"}, 1},
		"three actions":          {planOf("aws_launch_template.cp delete,create,update"), Person, []string{"module.spin.aws_launch_template.cp: delete, create, update"}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Decide(tc.plan, replaceable)
			if err != nil {
				t.Fatal(err)
			}
			if d.Verdict != tc.verdict || strings.Join(d.Held, ";") != strings.Join(tc.held, ";") || d.Changes != tc.changes {
				t.Errorf("%+v, want %s held %v changes %d", d, tc.verdict, tc.held, tc.changes)
			}
		})
	}
}

// What is not a plan's JSON - not JSON, an object with no format, a change with no actions - is
// an error, never nothing to do.
func TestWhatIsNotAPlanIsRefused(t *testing.T) {
	for name, raw := range map[string]string{
		"not JSON":      "{",
		"no format":     `{"resource_changes":[]}`,
		"no actions":    `{"format_version":"1.2","resource_changes":[{"address":"a.b","type":"a","change":{"actions":[]}}]}`,
		"a plan's text": "Plan: 1 to add, 0 to change, 0 to destroy.",
	} {
		if d, err := Decide([]byte(raw), nil); err == nil {
			t.Errorf("%s: %+v", name, d)
		}
	}
}
