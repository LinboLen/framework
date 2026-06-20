package console

import (
	"github.com/goravel/framework/console/console"
	"github.com/goravel/framework/contracts/binding"
	consolecontract "github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/foundation"
)

type ServiceProvider struct {
}

func (r *ServiceProvider) Relationship() binding.Relationship {
	return binding.Relationship{
		Bindings: []string{
			binding.Artisan,
		},
		Dependencies: binding.Bindings[binding.Artisan].Dependencies,
		ProvideFor:   []string{},
	}
}

func (r *ServiceProvider) Register(app foundation.Application) {
	app.Singleton(binding.Artisan, func(app foundation.Application) (any, error) {
		name := "artisan"
		usage := "Goravel Framework"
		usageText := "artisan command [options] [arguments...]"

		return NewApplication(name, usage, usageText, app.Version(), true), nil
	})
}

func (r *ServiceProvider) Boot(app foundation.Application) {
	artisanFacade := app.MakeArtisan()
	artisanFacade.Register([]consolecontract.Command{
		console.NewListCommand(),
	})
}
