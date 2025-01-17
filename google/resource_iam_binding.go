package google

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"
	"google.golang.org/api/cloudresourcemanager/v1"
)

var iamBindingSchema = map[string]*schema.Schema{
	"role": {
		Type:     schema.TypeString,
		Required: true,
		ForceNew: true,
	},
	"members": {
		Type:     schema.TypeSet,
		Required: true,
		Elem: &schema.Schema{
			Type:             schema.TypeString,
			DiffSuppressFunc: caseDiffSuppress,
		},
		Set: func(v interface{}) int {
			return schema.HashString(strings.ToLower(v.(string)))
		},
	},
	"etag": {
		Type:     schema.TypeString,
		Computed: true,
	},
}

func ResourceIamBinding(parentSpecificSchema map[string]*schema.Schema, newUpdaterFunc newResourceIamUpdaterFunc, resourceIdParser resourceIdParserFunc) *schema.Resource {
	return &schema.Resource{
		Create: resourceIamBindingCreateUpdate(newUpdaterFunc),
		Read:   resourceIamBindingRead(newUpdaterFunc),
		Update: resourceIamBindingCreateUpdate(newUpdaterFunc),
		Delete: resourceIamBindingDelete(newUpdaterFunc),
		Schema: mergeSchemas(iamBindingSchema, parentSpecificSchema),
		Importer: &schema.ResourceImporter{
			State: iamBindingImport(resourceIdParser),
		},
	}
}

func resourceIamBindingCreateUpdate(newUpdaterFunc newResourceIamUpdaterFunc) func(*schema.ResourceData, interface{}) error {
	return func(d *schema.ResourceData, meta interface{}) error {
		config := meta.(*Config)
		updater, err := newUpdaterFunc(d, config)
		if err != nil {
			return err
		}

		binding := getResourceIamBinding(d)
		err = iamPolicyReadModifyWrite(updater, func(ep *cloudresourcemanager.Policy) error {
			cleaned := removeAllBindingsWithRole(ep.Bindings, binding.Role)
			ep.Bindings = append(cleaned, binding)
			return nil
		})
		if err != nil {
			return err
		}
		d.SetId(updater.GetResourceId() + "/" + binding.Role)
		return resourceIamBindingRead(newUpdaterFunc)(d, meta)
	}
}

func resourceIamBindingRead(newUpdaterFunc newResourceIamUpdaterFunc) schema.ReadFunc {
	return func(d *schema.ResourceData, meta interface{}) error {
		config := meta.(*Config)
		updater, err := newUpdaterFunc(d, config)
		if err != nil {
			return err
		}

		eBinding := getResourceIamBinding(d)
		p, err := iamPolicyReadWithRetry(updater)
		if err != nil {
			return handleNotFoundError(err, d, fmt.Sprintf("Resource %q with IAM Binding (Role %q)", updater.DescribeResource(), eBinding.Role))
		}
		log.Printf("[DEBUG]: Retrieved policy for %s: %+v", updater.DescribeResource(), p)

		var binding *cloudresourcemanager.Binding
		for _, b := range p.Bindings {
			if b.Role != eBinding.Role {
				continue
			}
			binding = b
			break
		}
		if binding == nil {
			log.Printf("[DEBUG]: Binding for role %q not found in policy for %s, assuming it has no members.", eBinding.Role, updater.DescribeResource())
			d.Set("role", eBinding.Role)
			return nil
		} else {
			d.Set("role", binding.Role)
			d.Set("members", binding.Members)
		}
		d.Set("etag", p.Etag)
		return nil
	}
}

func iamBindingImport(resourceIdParser resourceIdParserFunc) schema.StateFunc {
	return func(d *schema.ResourceData, m interface{}) ([]*schema.ResourceData, error) {
		if resourceIdParser == nil {
			return nil, errors.New("Import not supported for this IAM resource.")
		}
		config := m.(*Config)
		s := strings.Fields(d.Id())
		if len(s) != 2 {
			d.SetId("")
			return nil, fmt.Errorf("Wrong number of parts to Binding id %s; expected 'resource_name role'.", s)
		}
		id, role := s[0], s[1]

		// Set the ID only to the first part so all IAM types can share the same resourceIdParserFunc.
		d.SetId(id)
		d.Set("role", role)
		err := resourceIdParser(d, config)
		if err != nil {
			return nil, err
		}

		// Set the ID again so that the ID matches the ID it would have if it had been created via TF.
		// Use the current ID in case it changed in the resourceIdParserFunc.
		d.SetId(d.Id() + "/" + role)
		// It is possible to return multiple bindings, since we can learn about all the bindings
		// for this resource here.  Unfortunately, `terraform import` has some messy behavior here -
		// there's no way to know at this point which resource is being imported, so it's not possible
		// to order this list in a useful way.  In the event of a complex set of bindings, the user
		// will have a terribly confusing set of imported resources and no way to know what matches
		// up to what.  And since the only users who will do a terraform import on their IAM bindings
		// are users who aren't too familiar with Google Cloud IAM (because a "create" for bindings or
		// members is idempotent), it's reasonable to expect that the user will be very alarmed by the
		// plan that terraform will output which mentions destroying a dozen-plus IAM bindings.  With
		// that in mind, we return only the binding that matters.
		return []*schema.ResourceData{d}, nil
	}
}

func resourceIamBindingDelete(newUpdaterFunc newResourceIamUpdaterFunc) schema.DeleteFunc {
	return func(d *schema.ResourceData, meta interface{}) error {
		config := meta.(*Config)
		updater, err := newUpdaterFunc(d, config)
		if err != nil {
			return err
		}

		binding := getResourceIamBinding(d)
		err = iamPolicyReadModifyWrite(updater, func(p *cloudresourcemanager.Policy) error {
			p.Bindings = removeAllBindingsWithRole(p.Bindings, binding.Role)
			return nil
		})
		if err != nil {
			return handleNotFoundError(err, d, fmt.Sprintf("Resource %q for IAM binding with role %q", updater.DescribeResource(), binding.Role))
		}

		return resourceIamBindingRead(newUpdaterFunc)(d, meta)
	}
}

func getResourceIamBinding(d *schema.ResourceData) *cloudresourcemanager.Binding {
	members := d.Get("members").(*schema.Set).List()
	return &cloudresourcemanager.Binding{
		Members: convertStringArr(members),
		Role:    d.Get("role").(string),
	}
}
