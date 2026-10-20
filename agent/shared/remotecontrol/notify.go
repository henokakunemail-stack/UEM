package remotecontrol

// sessionBanner is the notice the person at the endpoint sees while their
// screen is shared. Every remote-support product shows one: a session the user
// cannot see happening is indistinguishable from surveillance, and in most
// workplaces it is a policy violation even when the operator means well.
type sessionBanner interface {
	SetText(text string)
	Close()
}

func bannerText(operator, mode string) string {
	if operator == "" {
		operator = "an administrator"
	}
	if mode == "view_only" {
		return "Your screen is being viewed by " + operator
	}
	return "Your screen is being controlled by " + operator
}
