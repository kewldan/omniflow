package smartsub

// ViewServer is one server as the customer's screen lists it.
type ViewServer struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Down    bool   `json:"down"`
}

// ViewCountry is one country with its servers.
type ViewCountry struct {
	Code    string       `json:"code"`
	Servers []ViewServer `json:"servers"`
}

// View is the customer's own servers, grouped the way their subscription
// groups them, before their preferences apply: the screen shows everything
// they could turn on.
type View struct {
	Countries []ViewCountry `json:"countries"`
	Reserve   []ViewServer  `json:"reserve"`
}

// Entry is one server of a customer's subscription as the panel resolved it.
type Entry struct {
	Remark  string
	Address string
	Port    int
	Hidden  bool
}

// BuildView places a customer's servers for the screen.
func BuildView(entries []Entry, catalog Catalog, settings Settings) View {
	view := View{Countries: []ViewCountry{}, Reserve: []ViewServer{}}
	index := map[string]int{}
	var unknown *ViewCountry
	for _, entry := range entries {
		// A panel notice (a device limit, an expired subscription) is a server
		// at 0.0.0.0:1; it is a message, not a server to list.
		if entry.Hidden || entry.Address == "0.0.0.0" && entry.Port == 1 {
			continue
		}
		server := catalog.Classify(entry.Remark, entry.Address, entry.Port, settings)
		listed := ViewServer{Name: entry.Remark, Address: entry.Address, Port: entry.Port, Down: server.Down}
		switch server.Role {
		case RoleHidden:
			continue
		case RoleReserve:
			view.Reserve = append(view.Reserve, listed)
			continue
		}
		if server.Country == UnknownCountry {
			if unknown == nil {
				unknown = &ViewCountry{Code: UnknownCountry}
			}
			unknown.Servers = append(unknown.Servers, listed)
			continue
		}
		position, seen := index[server.Country]
		if !seen {
			position = len(view.Countries)
			index[server.Country] = position
			view.Countries = append(view.Countries, ViewCountry{Code: server.Country})
		}
		view.Countries[position].Servers = append(view.Countries[position].Servers, listed)
	}
	if unknown != nil {
		view.Countries = append(view.Countries, *unknown)
	}
	return view
}
