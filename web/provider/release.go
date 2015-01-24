package provider

import (
	"fmt"
	"time"

	"github.com/convox/kernel/web/Godeps/_workspace/src/github.com/crowdmob/goamz/dynamodb"
	"github.com/convox/kernel/web/Godeps/_workspace/src/github.com/goamz/goamz/cloudformation"
)

type Release struct {
	Id        string
	Ami       string
	CreatedAt time.Time
}

type UserdataParams struct {
	App     *AppParams
	Process string
	Env     map[string]string
	Ports   []int
}

func ReleaseList(cluster, app string) ([]Release, error) {
	table := releasesTable(cluster, app)

	q := dynamodb.NewQuery(table)
	q.AddIndex("app.created")
	q.AddKeyConditions([]dynamodb.AttributeComparison{
		*dynamodb.NewEqualStringAttributeComparison("app", app),
	})
	q.AddScanIndexForward(false)
	q.AddLimit(5)

	dreleases, _, err := table.QueryTable(q)

	if err != nil {
		return nil, err
	}

	releases := []Release{}

	for _, r := range dreleases {
		created, err := time.Parse(SortableTime, r["created"].Value)

		if err != nil {
			return nil, err
		}

		releases = append(releases, Release{
			Id:        coalesce(r["id"], ""),
			Ami:       coalesce(r["ami"], ""),
			CreatedAt: created,
		})
	}

	return releases, nil
}

func ReleaseCreate(cluster, app, ami string, options map[string]string) error {
	attributes := []dynamodb.Attribute{
		*dynamodb.NewStringAttribute("ami", ami),
		*dynamodb.NewStringAttribute("created-at", "now"),
	}

	for k, v := range options {
		attributes = append(attributes, *dynamodb.NewStringAttribute(k, v))
	}

	_, err := releasesTable(cluster, app).PutItem(ami, "", attributes)

	return err
}

func ReleasePromote(cluster, app, release string) error {
	formation, err := releaseFormation(cluster, app, release)

	if err != nil {
		fmt.Printf("err %+v\n", err)
	}

	a, err := AppShow(cluster, app)

	a.Repository = "https://github.com/convox-examples/sinatra.git"

	if err != nil {
		return err
	}

	stack := fmt.Sprintf("%s-%s", cluster, app)

	s, err := CloudFormation.DescribeStacks(stack, "")

	if err != nil {
		return err
	}

	params := &cloudformation.UpdateStackParams{
		StackName:    fmt.Sprintf("%s-%s", cluster, app),
		TemplateBody: formation,
		Parameters: append(s.Stacks[0].Parameters, cloudformation.Parameter{
			ParameterKey:   "Release",
			ParameterValue: release,
		}),
	}

	_, err = CloudFormation.UpdateStack(params)

	return err
}

func ReleaseCopy(cluster, app, release string) (string, error) {
	table := releasesTable(cluster, app)

	drel, err := table.GetItem(&dynamodb.Key{release, ""})
	fmt.Printf("drel %+v\n", drel)

	if err != nil {
		return "", err
	}

	rel := []dynamodb.Attribute{}
	id := generateId("R", 9)

	for key, attr := range drel {
		switch key {
		case "id":
			rel = append(rel, *dynamodb.NewStringAttribute(key, id))
		case "created":
			rel = append(rel, *dynamodb.NewStringAttribute(key, time.Now().Format(SortableTime)))
		default:
			rel = append(rel, *dynamodb.NewStringAttribute(key, attr.Value))
		}
	}

	_, err = releasesTable(cluster, app).PutItem(id, "", rel)

	return id, err
}

func releasesTable(cluster, app string) *dynamodb.Table {
	pk := dynamodb.PrimaryKey{dynamodb.NewStringAttribute("id", ""), nil}
	table := DynamoDB.NewTable(fmt.Sprintf("%s-%s-releases", cluster, app), pk)
	return table
}

func releaseFormation(cluster, app, release string) (string, error) {
	drelease, err := releasesTable(cluster, app).GetItem(&dynamodb.Key{release, ""})

	if err != nil {
		return "", err
	}

	ami := coalesce(drelease["ami"], "")

	if ami == "" {
		return "", fmt.Errorf("invalid ami")
	}

	params, err := appParams(cluster, app)

	if err != nil {
		return "", err
	}

	manifest, err := NewManifest(drelease["manifest"].Value)
	fmt.Printf("manifest %+v\n", manifest)
	fmt.Printf("err %+v\n", err)

	// manifest, err := releaseManifest(cluster, app, release)

	// if err != nil {
	//   return "", err
	// }

	// for name, process := range *manifest {
	//   if strings.HasPrefix(process.Image, "convox/") {
	//     key := process.Image[7:]

	//     rparams := ResourceParams{
	//       Name: fmt.Sprintf(upperName(key) + upperName(name)),
	//       Cidr: params.Cidr,
	//       Vpc:  params.Vpc,
	//     }

	//     resource, err := manifestResource(key, rparams)

	//     if err != nil {
	//       return "", err
	//     }

	//     params.Resources = append(params.Resources, *resource)
	//   }
	// }

	// params.Processes = []AppParamsProcess{}

	// for name, process := range *manifest {
	//   if !strings.HasPrefix(process.Image, "convox/") {
	//     uparams := UserdataParams{
	//       App:     params,
	//       Process: name,
	//       Env:     map[string]string{"FOO": "bar"},
	//       Ports:   []int{5000},
	//     }

	//     userdata, err := buildTemplate("userdata", uparams)

	//     printLines(userdata)

	//     if err != nil {
	//       return "", err
	//     }

	//     err = ProcessCreate(cluster, app, name)

	//     if err != nil {
	//       return "", err
	//     }

	//     params.Processes = append(params.Processes, AppParamsProcess{
	//       Ami:               ami,
	//       App:               app,
	//       AvailabilityZones: params.AvailabilityZones,
	//       Balancer:          (name == "web"),
	//       Cluster:           cluster,
	//       Count:             1,
	//       Name:              name,
	//       UserData:          userdata,
	//       Vpc:               params.Vpc,
	//     })
	//   }
	// }

	template, err := buildTemplate("app", params)

	printLines(template)

	if err != nil {
		return "", err
	}

	return template, nil
}

// type Manifest map[string]ManifestProcess

// func releaseManifest(cluster, app, release string) (*Manifest, error) {
//   drel, err := releasesTable(cluster, app).GetItem(&dynamodb.Key{release, ""})

//   if err != nil {
//     return nil, err
//   }

//   var manifest Manifest

//   err = yaml.Unmarshal([]byte(drel["manifest"].Value), &manifest)

//   return &manifest, err
// }

// func manifestResource(key string, params ResourceParams) (*Resource, error) {
//   env, err := buildResourceTemplate(key, "env", params)

//   if err != nil {
//     return nil, err
//   }

//   formation, err := buildResourceTemplate(key, "formation", params)

//   if err != nil {
//     return nil, err
//   }

//   return &Resource{Env: env, Formation: formation}, nil
// }