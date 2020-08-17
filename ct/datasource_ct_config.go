package ct

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-sdk/helper/hashcode"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"

	clct "github.com/coreos/container-linux-config-transpiler/config"
	fcct "github.com/coreos/fcct/config"
	"github.com/coreos/fcct/config/common"

	ignition "github.com/coreos/ignition/config/v2_3"
	ignitionTypes "github.com/coreos/ignition/config/v2_3/types"
	ignition30 "github.com/coreos/ignition/v2/config/v3_0"
	ignition30Types "github.com/coreos/ignition/v2/config/v3_0/types"
	ignition31 "github.com/coreos/ignition/v2/config/v3_1"
	ignition31Types "github.com/coreos/ignition/v2/config/v3_1/types"
)

func dataSourceCTConfig() *schema.Resource {
	return &schema.Resource{
		Read: dataSourceCTConfigRead,

		Schema: map[string]*schema.Schema{
			"content": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
			},
			"platform": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Default:  "",
				ForceNew: true,
			},
			"files_dir": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				Default:  "",
			},
			"snippets": &schema.Schema{
				Type: schema.TypeList,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Optional: true,
				ForceNew: true,
			},
			"pretty_print": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"strict": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"rendered": &schema.Schema{
				Type:        schema.TypeString,
				Computed:    true,
				Description: "rendered ignition configuration",
			},
		},
	}
}

func dataSourceCTConfigRead(d *schema.ResourceData, meta interface{}) error {
	rendered, err := renderConfig(d)
	if err != nil {
		return err
	}

	d.Set("rendered", rendered)
	d.SetId(strconv.Itoa(hashcode.String(rendered)))
	return nil
}

// Render a Fedora CoreOS Config or Container Linux Config as Ignition JSON.
func renderConfig(d *schema.ResourceData) (string, error) {
	// unchecked assertions seem to be the norm in Terraform :S
	content := d.Get("content").(string)
	platform := d.Get("platform").(string)
	filesDir := d.Get("files_dir").(string)
	snippetsIface := d.Get("snippets").([]interface{})
	pretty := d.Get("pretty_print").(bool)
	strict := d.Get("strict").(bool)

	snippets := make([]string, len(snippetsIface))
	for i, v := range snippetsIface {
		if v != nil {
			snippets[i] = v.(string)
		}
	}

	// Fedora CoreOS Config
	ign, err := fccToIgnition([]byte(content), filesDir, pretty, strict, snippets)
	if err == fcct.ErrNoVariant {
		// consider as Container Linux Config
		ign, err = renderCLC([]byte(content), platform, pretty, strict, snippets)
	}
	return string(ign), err
}

// Translate Fedora CoreOS config to Ignition v3.X.Y
func fccToIgnition(data []byte, filesDir string, pretty, strict bool, snippets []string) ([]byte, error) {
	ignBytes, _, err := fcct.Translate(data, common.TranslateOptions{
		Pretty: pretty,
		Strict: strict,
		BaseOptions: common.BaseOptions{
			FilesDir: filesDir,
		},
	})
	// ErrNoVariant indicates data is a CLC, not an FCC
	if err != nil {
		return nil, err
	}

	if len(snippets) == 0 {
		return ignBytes, nil
	}

	// merge FCC snippets into main Ignition config
	return mergeFCCSnippets(ignBytes, pretty, strict, snippets)
}

// Manually parse main Fedora CoreOS Config's Ignition using fallback Ignition
// versions. Then translate and parse FCC snippets as the chosen Ignition
// version to merge.
// version
// Upstream might later handle: https://github.com/coreos/fcct/issues/118
// Note: This means snippets version must match the main config version.
func mergeFCCSnippets(ignBytes []byte, pretty, strict bool, snippets []string) ([]byte, error) {

	ign, _, err := ignition31.Parse(ignBytes)
	if err == nil {
		// FCC config v1.1.0
		ign, err = mergeFCC11(ign, snippets, pretty, strict)
		if err != nil {
			return nil, fmt.Errorf("FCC v1.1.0 merge error: %v", err)
		}
		if pretty {
			return json.MarshalIndent(ign, "", "  ")
		}
		return json.Marshal(ign)
	}

	var ign30 ignition30Types.Config
	ign30, _, err = ignition30.Parse(ignBytes)
	if err != nil {
		return nil, fmt.Errorf("FCC v1.0.0 parse error: %v", err)
	}
	// FCC config v1.0.0
	ign30, err = mergeFCCV10(ign30, snippets, pretty, strict)
	if err != nil {
		return nil, fmt.Errorf("FCC v1.0.0 merge error: %v", err)
	}
	if pretty {
		return json.MarshalIndent(ign30, "", "  ")
	}
	return json.Marshal(ign30)
}

// merge FCC v1.1.0 snippets
func mergeFCC11(ign ignition31Types.Config, snippets []string, pretty, strict bool) (ignition31Types.Config, error) {
	for _, snippet := range snippets {
		ignextBytes, _, err := fcct.Translate([]byte(snippet), common.TranslateOptions{
			Pretty: pretty,
			Strict: strict,
		})
		if err != nil {
			// For FCC, require snippets be FCCs (don't fall-through to CLC)
			if err == fcct.ErrNoVariant {
				return ign, fmt.Errorf("Fedora CoreOS snippets require `variant`: %v", err)
			}
			return ign, fmt.Errorf("snippet v1.1.0 translate error: %v", err)
		}
		ignext, _, err := ignition31.Parse(ignextBytes)
		if err != nil {
			return ign, fmt.Errorf("snippet parse error: %v, expect v1.1.0", err)
		}
		ign = ignition31.Merge(ign, ignext)
	}
	return ign, nil
}

// merge FCC v1.0.0 snippets
func mergeFCCV10(ign ignition30Types.Config, snippets []string, pretty, strict bool) (ignition30Types.Config, error) {
	for _, snippet := range snippets {
		ignextBytes, _, err := fcct.Translate([]byte(snippet), common.TranslateOptions{
			Pretty: pretty,
			Strict: strict,
		})
		if err != nil {
			// For FCC, require snippets be FCCs (don't fall-through to CLC)
			if err == fcct.ErrNoVariant {
				return ign, fmt.Errorf("Fedora CoreOS snippets require `variant`: %v", err)
			}
			return ign, fmt.Errorf("snippet v1.0.0 translate error: %v", err)
		}
		ignext, _, err := ignition30.Parse(ignextBytes)
		if err != nil {
			return ign, fmt.Errorf("snippet parse error: %v, expect v1.0.0", err)
		}
		ign = ignition30.Merge(ign, ignext)
	}
	return ign, nil
}

// Translate Container Linux Config as Ignition JSON.
func renderCLC(data []byte, platform string, pretty, strict bool, snippets []string) ([]byte, error) {
	ign, err := clcToIgnition(data, platform, strict)
	if err != nil {
		return nil, err
	}

	for _, snippet := range snippets {
		ignext, err := clcToIgnition([]byte(snippet), platform, strict)
		if err != nil {
			return nil, err
		}
		ign = ignition.Append(ign, ignext)
	}

	if pretty {
		return json.MarshalIndent(ign, "", "  ")
	}
	return json.Marshal(ign)
}

// Parse Container Linux config and convert to Ignition v2.2.0 format.
func clcToIgnition(data []byte, platform string, strict bool) (ignitionTypes.Config, error) {
	// parse bytes into a Container Linux Config
	clc, ast, report := clct.Parse([]byte(data))

	if strict && len(report.Entries) > 0 {
		return ignitionTypes.Config{}, fmt.Errorf("error strict parsing Container Linux Config: %v", report.String())
	}

	if report.IsFatal() {
		return ignitionTypes.Config{}, fmt.Errorf("error parsing Container Linux Config: %v", report.String())
	}
	// convert Container Linux Config to an Ignition Config
	ign, report := clct.Convert(clc, platform, ast)
	if report.IsFatal() {
		return ignitionTypes.Config{}, fmt.Errorf("error converting to Ignition: %v", report.String())
	}
	return ign, nil
}
