package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go-pos-agent/internal/database"
	"go-pos-agent/internal/models"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

func RunAgent(userMessage string, apiKey string) (string, error) {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return "", err
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-2.0-flash-001")

	// --- 1. INTELLIGENCE UPGRADE: Improved Logic ---
	today := time.Now().Format("2006-01-02")

	// WE ADDED RULE #2 HERE TO FIX THE "COST" ISSUE
	systemPrompt := fmt.Sprintf(`SYSTEM: Today is %s. You are an Agentic POS Assistant.
	
	RULES:
	1. UPDATE: If a user asks to update a product by NAME (e.g. "Update Banana price"), you must NOT ask them for the ID. Instead:
	   - Call 'check_inventory' to find the ID.
	   - Call 'update_product_price' using that ID.
	
	2. READ: If a user asks for PRICE, COST, STOCK, or DETAILS of a product:
	   - You MUST call 'check_inventory' to get the full list.
	   - Then read the JSON to find the specific item and answer the user.
	   - Do NOT say "I cannot get the price". You CAN get it by checking inventory.
	
	3. SALES: If the user asks for sales/revenue, use 'get_sales_report'.
	
	USER: %s`, today, userMessage)

	// --- DEFINE TOOLS ---
	model.Tools = []*genai.Tool{
		{
			FunctionDeclarations: []*genai.FunctionDeclaration{
				{
					Name: "check_inventory",
					// Updated description to be more "inviting" for price checks
					Description: "Get the full inventory list. Use this to find ANY product details like ID, Name, Price, Cost, or Stock.",
				},
				{
					Name:        "update_product_price",
					Description: "Update the price of a specific product using its ID",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"product_id": {Type: genai.TypeInteger, Description: "ID of the product"},
							"new_price":  {Type: genai.TypeNumber, Description: "New price"},
						},
						Required: []string{"product_id", "new_price"},
					},
				},
				{
					Name:        "create_product",
					Description: "Add a new product to the inventory",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"name":           {Type: genai.TypeString, Description: "Name of the product"},
							"price":          {Type: genai.TypeNumber, Description: "Price of the product"},
							"category":       {Type: genai.TypeString, Description: "Category (Food, Drink, etc)"},
							"stock_quantity": {Type: genai.TypeInteger, Description: "Initial stock count"},
						},
						Required: []string{"name", "price", "category", "stock_quantity"},
					},
				},
				{
					Name:        "get_sales_report",
					Description: "Get total sales revenue for a date range.",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"start_date": {Type: genai.TypeString, Description: "Start date (YYYY-MM-DD)"},
							"end_date":   {Type: genai.TypeString, Description: "End date (YYYY-MM-DD)"},
						},
						Required: []string{"start_date", "end_date"},
					},
				},
			},
		},
	}

	session := model.StartChat()

	resp, err := session.SendMessage(ctx, genai.Text(systemPrompt))
	if err != nil {
		return "", err
	}

	// --- HANDLE TOOL CALLS ---
	for _, part := range resp.Candidates[0].Content.Parts {
		if funcCall, ok := part.(genai.FunctionCall); ok {

			// TOOL 1: Check Inventory
			if funcCall.Name == "check_inventory" {
				var products []models.Product
				database.DB.Find(&products)

				type SimpleProduct struct {
					ID    uint    `json:"id"`
					Name  string  `json:"name"`
					Stock int     `json:"stock"`
					Price float64 `json:"price"`
				}
				var simpleList []SimpleProduct
				for _, p := range products {
					simpleList = append(simpleList, SimpleProduct{
						ID:    p.ID,
						Name:  p.Name,
						Stock: p.StockQuantity,
						Price: p.Price,
					})
				}

				jsonBytes, _ := json.Marshal(simpleList)

				toolResp := genai.FunctionResponse{
					Name:     "check_inventory",
					Response: map[string]interface{}{"inventory": string(jsonBytes)},
				}

				finalResp, err := session.SendMessage(ctx, toolResp)
				if err != nil {
					return "", err
				}

				return handleRecursiveToolCalls(ctx, session, finalResp), nil
			}

			// TOOL 2: Update Price
			if funcCall.Name == "update_product_price" {
				return executeUpdatePrice(ctx, session, funcCall), nil
			}

			// TOOL 3: Create Product
			if funcCall.Name == "create_product" {
				return executeCreateProduct(ctx, session, funcCall), nil
			}

			// TOOL 4: Sales Report
			if funcCall.Name == "get_sales_report" {
				return executeSalesReport(ctx, session, funcCall), nil
			}
		}
	}

	return printResponse(resp), nil
}

// --- HELPER FUNCTIONS ---

func handleRecursiveToolCalls(ctx context.Context, session *genai.ChatSession, resp *genai.GenerateContentResponse) string {
	for _, part := range resp.Candidates[0].Content.Parts {
		if funcCall, ok := part.(genai.FunctionCall); ok {
			if funcCall.Name == "update_product_price" {
				return executeUpdatePrice(ctx, session, funcCall)
			}
		}
	}
	return printResponse(resp)
}

func executeUpdatePrice(ctx context.Context, session *genai.ChatSession, funcCall genai.FunctionCall) string {
	args := funcCall.Args
	productID := int(args["product_id"].(float64))
	newPrice := args["new_price"].(float64)

	result := database.DB.Model(&models.Product{}).Where("id = ?", productID).Update("price", newPrice)

	msg := "Success"
	if result.RowsAffected == 0 {
		msg = "Product ID not found"
	}

	finalResp, _ := session.SendMessage(ctx, genai.FunctionResponse{
		Name:     "update_product_price",
		Response: map[string]interface{}{"status": msg, "new_price": newPrice},
	})
	return printResponse(finalResp)
}

func executeCreateProduct(ctx context.Context, session *genai.ChatSession, funcCall genai.FunctionCall) string {
	args := funcCall.Args
	newProd := models.Product{
		Name:          args["name"].(string),
		Price:         args["price"].(float64),
		Category:      args["category"].(string),
		StockQuantity: int(args["stock_quantity"].(float64)),
		ImageURL:      "https://via.placeholder.com/150",
	}
	database.DB.Create(&newProd)
	finalResp, _ := session.SendMessage(ctx, genai.FunctionResponse{
		Name:     "create_product",
		Response: map[string]interface{}{"status": "created", "id": newProd.ID},
	})
	return printResponse(finalResp)
}

func executeSalesReport(ctx context.Context, session *genai.ChatSession, funcCall genai.FunctionCall) string {
	args := funcCall.Args
	startStr := args["start_date"].(string)
	endStr := args["end_date"].(string)

	start, err1 := time.Parse("2006-01-02", startStr)
	end, err2 := time.Parse("2006-01-02", endStr)

	if err1 != nil || err2 != nil {
		return "Error: Dates must be in YYYY-MM-DD format."
	}
	end = end.Add(23*time.Hour + 59*time.Minute)

	report, err := database.GetSalesReport(start, end)
	if err != nil {
		return "Error calculating sales."
	}

	finalResp, _ := session.SendMessage(ctx, genai.FunctionResponse{
		Name: "get_sales_report",
		Response: map[string]interface{}{
			"revenue":     report.TotalRevenue,
			"sales_count": report.TotalCount,
		},
	})
	return printResponse(finalResp)
}

func printResponse(resp *genai.GenerateContentResponse) string {
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			return string(txt)
		}
	}
	return "I completed the action."
}
