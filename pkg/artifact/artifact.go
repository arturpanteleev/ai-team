package artifact

import "fmt"

type Task struct {
	Feature    string
	TaskDesc   string
	ArtifactRoot string
}

type Paths struct {
	FeatureName string
	Root        string
}

func NewPaths(feature, root string) *Paths {
	return &Paths{FeatureName: feature, Root: root}
}

func (p *Paths) Task() string {
	return fmt.Sprintf("%s/tasks/%s/task.md", p.Root, p.FeatureName)
}

func (p *Paths) ProductSpec() string {
	return fmt.Sprintf("%s/product/%s/spec.md", p.Root, p.FeatureName)
}

func (p *Paths) TechDesign() string {
	return fmt.Sprintf("%s/tech/%s/design.md", p.Root, p.FeatureName)
}

func (p *Paths) Review() string {
	return fmt.Sprintf("%s/reviews/%s/review.md", p.Root, p.FeatureName)
}

func (p *Paths) TestReport() string {
	return fmt.Sprintf("%s/reviews/%s/test-report.md", p.Root, p.FeatureName)
}

func (p *Paths) Proposal() string {
	return fmt.Sprintf("%s/%s/proposal.md", p.Root, p.FeatureName)
}
