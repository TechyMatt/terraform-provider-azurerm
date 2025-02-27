package signalr

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonids"
	"github.com/hashicorp/go-azure-sdk/resource-manager/webpubsub/2023-02-01/webpubsub"
	"github.com/hashicorp/terraform-provider-azurerm/internal/sdk"
	keyVaultParse "github.com/hashicorp/terraform-provider-azurerm/internal/services/keyvault/parse"
	keyVaultValidate "github.com/hashicorp/terraform-provider-azurerm/internal/services/keyvault/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
)

type CustomCertWebPubsubModel struct {
	Name               string `tfschema:"name"`
	WebPubsubId        string `tfschema:"web_pubsub_id"`
	CustomCertId       string `tfschema:"custom_certificate_id"`
	CertificateVersion string `tfschema:"certificate_version"`
}

type CustomCertWebPubsubResource struct{}

var _ sdk.Resource = CustomCertWebPubsubResource{}

func (r CustomCertWebPubsubResource) Arguments() map[string]*pluginsdk.Schema {
	return map[string]*pluginsdk.Schema{
		"name": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: validation.StringIsNotEmpty,
		},

		"web_pubsub_id": {
			Type:         pluginsdk.TypeString,
			Required:     true,
			ForceNew:     true,
			ValidateFunc: webpubsub.ValidateWebPubSubID,
		},

		"custom_certificate_id": {
			Type:     pluginsdk.TypeString,
			Required: true,
			ForceNew: true,
			ValidateFunc: validation.Any(
				keyVaultValidate.NestedItemId,
				keyVaultValidate.NestedItemIdWithOptionalVersion,
			),
		},
	}
}

func (r CustomCertWebPubsubResource) Attributes() map[string]*pluginsdk.Schema {
	return map[string]*pluginsdk.Schema{
		"certificate_version": {
			Type:     pluginsdk.TypeString,
			Computed: true,
		},
	}
}

func (r CustomCertWebPubsubResource) ModelObject() interface{} {
	return &CustomCertWebPubsubModel{}
}

func (r CustomCertWebPubsubResource) ResourceType() string {
	return "azurerm_web_pubsub_custom_certificate"
}

func (r CustomCertWebPubsubResource) Create() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 30 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			var customCertWebPubsub CustomCertWebPubsubModel
			if err := metadata.Decode(&customCertWebPubsub); err != nil {
				return fmt.Errorf("decoding: %+v", err)
			}
			client := metadata.Client.SignalR.WebPubSubClient.WebPubSub

			webPubsubId, err := webpubsub.ParseWebPubSubID(metadata.ResourceData.Get("web_pubsub_id").(string))
			if err != nil {
				return fmt.Errorf("parsing web pubsub service id error: %+v", err)
			}

			keyVaultCertificateId, err := keyVaultParse.ParseOptionallyVersionedNestedItemID(metadata.ResourceData.Get("custom_certificate_id").(string))
			if err != nil {
				return fmt.Errorf("parsing custom certificate id error: %+v", err)
			}

			keyVaultUri := keyVaultCertificateId.KeyVaultBaseUrl
			keyVaultSecretName := keyVaultCertificateId.Name

			id := webpubsub.NewCustomCertificateID(webPubsubId.SubscriptionId, webPubsubId.ResourceGroupName, webPubsubId.WebPubSubName, customCertWebPubsub.Name)

			existing, err := client.CustomCertificatesGet(ctx, id)
			if err != nil && !response.WasNotFound(existing.HttpResponse) {
				return fmt.Errorf("checking for existing %s: %+v", id, err)
			}

			if !response.WasNotFound(existing.HttpResponse) {
				return metadata.ResourceRequiresImport(r.ResourceType(), id)
			}

			customCertObj := webpubsub.CustomCertificate{
				Properties: webpubsub.CustomCertificateProperties{
					KeyVaultBaseUri:    keyVaultUri,
					KeyVaultSecretName: keyVaultSecretName,
				},
			}
			if keyVaultCertificateId.Version != "" {
				customCertObj.Properties.KeyVaultSecretVersion = utils.String(keyVaultCertificateId.Version)

			}

			if err := client.CustomCertificatesCreateOrUpdateThenPoll(ctx, id, customCertObj); err != nil {
				return fmt.Errorf("creating web pubsub custom certificate: %s: %+v", id, err)
			}

			metadata.SetID(id)
			return nil
		},
	}
}

func (r CustomCertWebPubsubResource) Read() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 5 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			client := metadata.Client.SignalR.WebPubSubClient.WebPubSub
			keyVaultClient := metadata.Client.KeyVault
			resourcesClient := metadata.Client.Resource
			id, err := webpubsub.ParseCustomCertificateID(metadata.ResourceData.Id())
			if err != nil {
				return err
			}

			resp, err := client.CustomCertificatesGet(ctx, *id)
			if err != nil {
				if response.WasNotFound(resp.HttpResponse) {
					return metadata.MarkAsGone(id)
				}
				return fmt.Errorf("retrieving %s: %+v", *id, err)
			}

			if resp.Model == nil {
				return fmt.Errorf("retrieving %s: got nil model", *id)
			}

			vaultBasedUri := resp.Model.Properties.KeyVaultBaseUri
			certName := resp.Model.Properties.KeyVaultSecretName

			keyVaultIdRaw, err := keyVaultClient.KeyVaultIDFromBaseUrl(ctx, resourcesClient, vaultBasedUri)
			if err != nil {
				return fmt.Errorf("getting key vault base uri from %s: %+v", id, err)
			}
			vaultId, err := commonids.ParseKeyVaultID(*keyVaultIdRaw)
			if err != nil {
				return fmt.Errorf("parsing key vault %s: %+v", vaultId, err)
			}

			certVersion := ""
			if resp.Model.Properties.KeyVaultSecretVersion != nil {
				certVersion = *resp.Model.Properties.KeyVaultSecretVersion
			}
			nestedItem, err := keyVaultParse.NewNestedItemID(vaultBasedUri, "certificates", certName, certVersion)
			if err != nil {
				return err
			}

			certId := nestedItem.ID()

			state := CustomCertWebPubsubModel{
				Name:               id.CustomCertificateName,
				CustomCertId:       certId,
				WebPubsubId:        webpubsub.NewWebPubSubID(id.SubscriptionId, id.ResourceGroupName, id.WebPubSubName).ID(),
				CertificateVersion: utils.NormalizeNilableString(resp.Model.Properties.KeyVaultSecretVersion),
			}

			return metadata.Encode(&state)
		},
	}
}

func (r CustomCertWebPubsubResource) Delete() sdk.ResourceFunc {
	return sdk.ResourceFunc{
		Timeout: 30 * time.Minute,
		Func: func(ctx context.Context, metadata sdk.ResourceMetaData) error {
			client := metadata.Client.SignalR.WebPubSubClient.WebPubSub

			id, err := webpubsub.ParseCustomCertificateID(metadata.ResourceData.Id())
			if err != nil {
				return err
			}
			if _, err := client.CustomCertificatesDelete(ctx, *id); err != nil {
				return fmt.Errorf("deleting %s: %+v", id, err)
			}
			return nil
		},
	}
}

func (r CustomCertWebPubsubResource) IDValidationFunc() pluginsdk.SchemaValidateFunc {
	return webpubsub.ValidateCustomCertificateID
}
