package claude

import "github.com/thorstenhirsch/clue/provider"

type Provider struct{}

func NewProvider() *Provider { return &Provider{} }

func (*Provider) CheckCredentials() error {
	_, err := LoadCredentials()
	return err
}

func (*Provider) FetchUsage() (*provider.Usage, error) {
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	return NewClient(creds.AccessToken).FetchUsage()
}
