package amazon

import "os"

func FromEnv() (*Client, error) {
	from := os.Getenv("AMAZON_PUNCHOUT_FROM_IDENTITY")
	secret := os.Getenv("AMAZON_PUNCHOUT_SHARED_SECRET")
	if from == "" && secret == "" {
		return nil, nil
	}
	return New(Config{
		FromIdentity: from,
		SharedSecret: secret,
		TestURL:      "https://abintegrations.amazon.de/punchout/test",
		LiveURL:      "https://abintegrations.amazon.de/punchout",
		OrderURL:     os.Getenv("AMAZON_PUNCHOUT_ORDER_URL"),
		ReturnURL:    os.Getenv("AMAZON_PUNCHOUT_RETURN_URL"),
		Mode:         os.Getenv("AMAZON_PUNCHOUT_MODE"),
		ShipTo: Address{
			Company:    os.Getenv("AMAZON_SHIP_TO_COMPANY"),
			Recipient:  os.Getenv("AMAZON_SHIP_TO_RECIPIENT"),
			Street:     os.Getenv("AMAZON_SHIP_TO_STREET"),
			City:       os.Getenv("AMAZON_SHIP_TO_CITY"),
			Region:     os.Getenv("AMAZON_SHIP_TO_REGION"),
			PostalCode: os.Getenv("AMAZON_SHIP_TO_POSTAL_CODE"),
			Country:    os.Getenv("AMAZON_SHIP_TO_COUNTRY"),
			Email:      os.Getenv("AMAZON_SHIP_TO_EMAIL"),
		},
		BillTo: Address{
			Company:    os.Getenv("AMAZON_BILL_TO_COMPANY"),
			Recipient:  os.Getenv("AMAZON_BILL_TO_RECIPIENT"),
			Street:     os.Getenv("AMAZON_BILL_TO_STREET"),
			City:       os.Getenv("AMAZON_BILL_TO_CITY"),
			Region:     os.Getenv("AMAZON_BILL_TO_REGION"),
			PostalCode: os.Getenv("AMAZON_BILL_TO_POSTAL_CODE"),
			Country:    os.Getenv("AMAZON_BILL_TO_COUNTRY"),
			Email:      os.Getenv("AMAZON_BILL_TO_EMAIL"),
		},
	})
}
