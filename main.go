package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"stock-see/eval"
	"stock-see/evalintent"
	"stock-see/intent"
	"stock-see/intent/easyrules"
	"stock-see/rag"
	"stock-see/router"
	"stock-see/tools"

	"github.com/cloudwego/eino/adk"

	"github.com/cloudwego/eino-ext/components/model/openai" //openai模型
	"github.com/cloudwego/eino/compose"
)

func main() {
	evalFlag := flag.Bool("eval", false, "运行离线评测集（读取 -eval-suite 或 config.eval.defaultSuitePath），打印均分后退出")
	evalSuite := flag.String("eval-suite", "", "评测集 JSON 路径（默认可由 config eval.defaultSuitePath 指定）")
	evalJSONOut := flag.String("eval-json", "", "评测汇总写入该 JSON 文件（可选）")
	evalIntentFlag := flag.Bool("eval-intent", false, "运行意图评测集，打印准确率与均分后退出")
	evalIntentSuite := flag.String("eval-intent-suite", "", "意图评测集 JSON（默认 config eval.defaultIntentSuitePath 或 data/eval/intent_suite.json）")
	evalIntentJSONOut := flag.String("eval-intent-json", "", "意图评测汇总写入该 JSON 文件（可选）")
	evalIntentMode := flag.String("eval-intent-mode", "fc", "解析路径：fc=仅模型 FC；combo=词典+规则；pipeline=与线上一致（高置信槽位跳过 FC）")
	evalIntentVerbose := flag.Bool("eval-intent-verbose", false, "逐条打印未满分用例的 Notes")
	evalRetrievalFlag := flag.Bool("eval-retrieval", false, "运行检索评测（Redis+embedding），对比 vector / hybrid / hybrid_rerank 的 Hit@K 后退出（无需对话模型）")
	evalRetrievalSuite := flag.String("eval-retrieval-suite", "", "检索评测集 JSON（默认 config eval.defaultRetrievalSuitePath 或 data/eval/retrieval_suite.json）")
	evalRetrievalJSONOut := flag.String("eval-retrieval-json", "", "检索评测汇总写入该 JSON（可选）")
	evalRetrievalVerbose := flag.Bool("eval-retrieval-verbose", false, "检索评测输出逐条 Top 标题与是否命中")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	chatCfg := tools.GetChatOpenAIConfig()
	if chatCfg == nil || chatCfg.Model == "" || chatCfg.APIKey == "" {
		panic(fmt.Errorf("请在 config/stock.json 或 config/stock.example.json 中配置 chatOpenAI（model、apiKey、baseURL）"))
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL: chatCfg.BaseURL,
		Model:   chatCfg.Model,
		APIKey:  chatCfg.APIKey,
	})
	if err != nil {
		panic(fmt.Errorf("初始化模型失败: %v", err))
	}

	sysTpl, promptVer, err := tools.GetResolvedPrompt()
	if err != nil {
		panic(fmt.Errorf("解析 prompt 配置失败: %v", err))
	}
	log.Printf("prompt 当前版本: %s", promptVer)

	if *evalRetrievalFlag {
		suitePath := strings.TrimSpace(*evalRetrievalSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultRetrievalSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/retrieval_suite.json"
		}
		rc, err := rag.NewWithRedisFromEnv("")
		if err != nil {
			log.Fatalf("检索评测需要 Redis: %v", err)
		}
		sum, err := rag.RunRetrievalEval(ctx, rc, suitePath, *evalRetrievalVerbose)
		if err != nil {
			log.Fatalf("检索评测失败: %v", err)
		}
		for _, m := range sum.ByMode {
			log.Printf("检索评测 [%s] Hit@%d=%.2f%% (%d/%d)", m.Mode, m.K, m.HitRate*100, m.HitCount, m.Cases)
		}
		if p := strings.TrimSpace(*evalRetrievalJSONOut); p != "" {
			b, _ := json.MarshalIndent(sum, "", "  ")
			if err := os.WriteFile(p, b, 0644); err != nil {
				log.Fatalf("写入 %s: %v", p, err)
			}
			log.Printf("已写入 %s", p)
		}
		os.Exit(0)
	}

	if *evalFlag {
		suitePath := strings.TrimSpace(*evalSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/suite.json"
		}
		sum, err := eval.Run(ctx, chatModel, eval.Options{
			SuitePath:      suitePath,
			SystemTemplate: sysTpl,
			PromptVersion:  promptVer,
		})
		if err != nil {
			log.Fatalf("评测失败: %v", err)
		}
		log.Printf("评测完成 prompt=%s suite=%s 平均分=%.2f / 100", sum.PromptVersion, sum.SuitePath, sum.Average)
		if p := strings.TrimSpace(*evalJSONOut); p != "" {
			b, _ := json.MarshalIndent(sum, "", "  ")
			if err := os.WriteFile(p, b, 0644); err != nil {
				log.Fatalf("写入 %s: %v", p, err)
			}
			log.Printf("已写入 %s", p)
		}
		os.Exit(0)
	}

	if *evalIntentFlag {
		suitePath := strings.TrimSpace(*evalIntentSuite)
		if suitePath == "" {
			suitePath = tools.GetEvalDefaultIntentSuitePath()
		}
		if suitePath == "" {
			suitePath = "data/eval/intent_suite.json"
		}
		mode := strings.TrimSpace(*evalIntentMode)
		var predict intent.EvalPredictor
		switch mode {
		case "combo":
			predict = evalintent.PredictCombo()
		case "pipeline":
			predict = evalintent.PredictPipeline(chatModel)
		default:
			mode = "fc"
			predict = evalintent.PredictFC(chatModel)
		}
		sum, err := intent.RunEvalWithPredictor(ctx, suitePath, mode, predict)
		if err != nil {
			log.Fatalf("意图评测失败: %v", err)
		}
		axisPart := "compare_axis=n/a"
		if sum.CompareAxisCases > 0 {
			axisPart = fmt.Sprintf("compare_axis=%.2f%%（n=%d）", sum.CompareAxisAccuracy*100, sum.CompareAxisCases)
		}
		log.Printf("意图评测 suite=%s mode=%s evaluatedAt=%s 用例数=%d task=%.2f%% symbols=%.2f%%（n=%d） hints=%.2f%%（n=%d） %s 均分=%.2f",
			sum.SuitePath, sum.Mode, sum.EvaluatedAt, sum.Total,
			sum.TaskAccuracy*100, sum.SymbolAccuracy*100, sum.SymbolCases,
			sum.HintAccuracy*100, sum.HintCases, axisPart, sum.AverageScore)
		for _, b := range sum.ByExpectedTask {
			log.Printf("  └─ 期望 task_kind=%s: %.2f%% (%d/%d)", b.ExpectedKind, b.Accuracy*100, b.TaskHits, b.Count)
		}
		if *evalIntentVerbose {
			for _, r := range sum.Results {
				if r.Score >= 1 {
					continue
				}
				log.Printf("[eval-intent] id=%s score=%.2f task=%v syms=%v hints=%v axis=%v notes=%s",
					r.ID, r.Score, r.OKTask, r.OKSymbols, r.OKHints, r.OKCompareAxis, r.Notes)
			}
		}
		if p := strings.TrimSpace(*evalIntentJSONOut); p != "" {
			b, _ := json.MarshalIndent(sum, "", "  ")
			if err := os.WriteFile(p, b, 0644); err != nil {
				log.Fatalf("写入 %s: %v", p, err)
			}
			log.Printf("已写入 %s", p)
		}
		os.Exit(0)
	}

	agentConfig := &adk.ChatModelAgentConfig{
		Name:        "StockAssistant",
		Description: "股票分析助手",
		Instruction: sysTpl,
		Model:       chatModel,
	}
	agentConfig.ToolsConfig = adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools.StockTools(),
		},
	}

	agent, err := adk.NewChatModelAgent(ctx, agentConfig)
	if err != nil {
		panic(fmt.Errorf("创建 ChatModelAgent 失败: %v", err))
	}

	// Runner 用于注入会话上下文（SessionValues），使 Instruction 中的 {Context} 等占位符生效
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
	})

	mux := router.InitRouter(runner, chatModel)
	srv := &http.Server{Addr: ":8080", Handler: mux}
	// 定时任务：每小时拉取新闻写入 RAG（Redis 未配置时自动跳过）
	go router.RunRAGTicker()

	if tools.IntentEasyRulesEnabled() {
		if err := easyrules.Load(tools.IntentRulesFilePath()); err != nil {
			log.Printf("[easyrules] 初始加载失败（仅槽位组合生效）: %v", err)
		}
	}

	log.Println("Server started on port 8080")
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(fmt.Errorf("listen and serve: %v", err))
		}
	}()
	<-ctx.Done()
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer c2()
	_ = srv.Shutdown(shutdownCtx)
}
