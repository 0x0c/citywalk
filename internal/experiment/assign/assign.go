package assign

// Result is the outcome of assigning one identity within one experiment.
type Result struct {
	Bucket    int
	Variant   string
	IsHoldout bool
}

// Assign buckets identity under salt and looks the bucket up in table — CW-0008 Units 2 and 3
// composed into the one operation a payload assembly or an analysis pipeline calls. The result is
// deterministic: the same table, salt, and identity always produce the same Result, computed fresh
// each time rather than read from anywhere.
func Assign(table RangeTable, salt, identity string) (Result, error) {
	bucket := Bucket(salt, identity)
	variant, isHoldout, err := table.Lookup(bucket)
	if err != nil {
		return Result{}, err
	}
	return Result{Bucket: bucket, Variant: variant, IsHoldout: isHoldout}, nil
}

// InProjectHoldout reports whether identity falls in the project-wide control group (CW-0008 Unit
// 4): a fixed salt per project, separate from any experiment's salt, so a user's membership in it
// does not depend on — and is not correlated with — any single campaign's own holdout.
// holdoutBucketCount is the number of buckets (out of BucketCount) the control group reserves.
func InProjectHoldout(projectSalt string, holdoutBucketCount int, identity string) bool {
	return Bucket(projectSalt, identity) < holdoutBucketCount
}
