package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	baseURL  = "http://nightingale/targets"
	username = ""
	password = ""

	// MinIO配置
	minioEndpoint  = ""
	minioAccessKey = ""
	minioSecretKey = ""
	minioBucket    = "screenshots"
	minioUseSSL    = false
)

func main() {
	// 定时任务 - 每1小时执行一次
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 立即执行一次
	runScreenshotTask()

	// 定时执行
	for {
		select {
		case <-ticker.C:
			runScreenshotTask()
		}
	}
}

// runScreenshotTask 执行截图任务
func runScreenshotTask() {
	fmt.Printf("[%s] 开始执行截图任务...\n", time.Now().Format("2006-01-02 15:04:05"))

	// 创建ChromeDP选项
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"),
		chromedp.Flag("headless", false), // 可见模式
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor"),
		chromedp.WindowSize(1920, 600), // 使用较小的窗口高度强制滚动
	)

	// 创建ChromeDP上下文
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 执行登录和分页设置
	err := chromedp.Run(ctx,
		// 访问网页
		chromedp.Navigate(baseURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Sleep(5*time.Second),

		// 登录流程
		chromedp.ActionFunc(func(ctx context.Context) error {
			return performLogin(ctx)
		}),

		// 设置分页
		chromedp.ActionFunc(func(ctx context.Context) error {
			return setPagination(ctx)
		}),
	)

	if err != nil {
		fmt.Printf("页面设置失败: %v\n", err)
		return
	}

	// 等待页面完全加载
	fmt.Println("等待页面完全加载...")
	chromedp.Sleep(3 * time.Second).Do(ctx)

	// 获取页面信息
	var pageHeight, viewportHeight int
	chromedp.Run(ctx, chromedp.Evaluate(`Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)`, &pageHeight))
	chromedp.Run(ctx, chromedp.Evaluate(`window.innerHeight`, &viewportHeight))

	fmt.Printf("页面总高度: %d 像素\n", pageHeight)
	fmt.Printf("视口高度: %d 像素\n", viewportHeight)

	// 强制设置页面高度以获取完整内容
	fmt.Println("强制设置页面高度...")
	chromedp.Run(ctx, chromedp.Evaluate(`
		// 尝试移除高度限制
		document.body.style.height = 'auto';
		document.documentElement.style.height = 'auto';
		
		// 查找可能的容器并移除高度限制
		const containers = document.querySelectorAll('*[style*="height"]');
		containers.forEach(container => {
			container.style.height = 'auto';
		});
		
		// 强制重新计算高度
		document.body.offsetHeight;
	`, nil))
	chromedp.Sleep(2 * time.Second).Do(ctx)

	// 重新检查页面高度
	var newPageHeight int
	chromedp.Run(ctx, chromedp.Evaluate(`Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)`, &newPageHeight))
	fmt.Printf("强制设置后页面高度: %d 像素\n", newPageHeight)

	// 使用FullScreenshot获取完整页面
	fmt.Println("使用FullScreenshot获取完整页面...")
	var fullPageBuf []byte
	err = chromedp.Run(ctx, chromedp.FullScreenshot(&fullPageBuf, 90))
	if err != nil {
		fmt.Printf("FullScreenshot失败: %v\n", err)
		return
	}

	// 保存截图
	filename := "force_scroll_fullscreen.png"
	err = saveScreenshot(fullPageBuf, filename)
	if err != nil {
		fmt.Printf("保存截图失败: %v\n", err)
		return
	}
	fmt.Printf("✅ 截图已保存: %s (大小: %d bytes)\n", filename, len(fullPageBuf))

	// 上传到MinIO
	fmt.Println("上传截图到MinIO...")
	err = uploadToMinIO(filename)
	if err != nil {
		fmt.Printf("上传到MinIO失败: %v\n", err)
	} else {
		fmt.Println("✅ 上传到MinIO成功")
	}

	// 清理其他图片文件
	fmt.Println("清理临时图片文件...")
	cleanupImages(filename)

	fmt.Printf("[%s] 截图任务完成\n", time.Now().Format("2006-01-02 15:04:05"))
}

// saveScreenshot 保存截图到文件
func saveScreenshot(data []byte, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(data)
	return err
}

// performLogin 执行登录流程
func performLogin(ctx context.Context) error {
	fmt.Println("正在执行登录...")

	// 查找并填写认证信息
	fmt.Println("正在查找认证表单...")

	// 检查是否有用户名输入框
	var hasUsername bool
	err := chromedp.Run(ctx,
		chromedp.Evaluate(`
			document.querySelector('input[type="text"]') !== null || 
			document.querySelector('input[name="username"]') !== null || 
			document.querySelector('input[name="user"]') !== null ||
			document.querySelector('input[id*="user"]') !== null ||
			document.querySelector('input[id="username"]') !== null ||
			document.querySelector('input[placeholder*="用户名"]') !== null ||
			document.querySelector('input[placeholder*="username" i]') !== null
		`, &hasUsername),
	)
	if err != nil {
		fmt.Printf("检查用户名输入框失败: %v\n", err)
		return err
	}

	// 检查是否有密码输入框
	var hasPassword bool
	err = chromedp.Run(ctx,
		chromedp.Evaluate(`
			document.querySelector('input[type="password"]') !== null ||
			document.querySelector('input[placeholder*="密码"]') !== null ||
			document.querySelector('input[placeholder*="password" i]') !== null ||
			document.querySelector('input[id*="password"]') !== null ||
			document.querySelector('input[id="password"]') !== null
		`, &hasPassword),
	)
	if err != nil {
		fmt.Printf("检查密码输入框失败: %v\n", err)
		return err
	}

	fmt.Printf("找到用户名输入框: %v\n", hasUsername)
	fmt.Printf("找到密码输入框: %v\n", hasPassword)

	if hasUsername && hasPassword {
		fmt.Println("开始填写认证信息...")

		// 填写用户名
		usernameSelectors := []string{
			`input[id="username"]`,
			`input[placeholder="请输入用户名"]`,
			`input[placeholder*="用户名"]`,
			`input[placeholder*="username" i]`,
			`input[type="text"]`,
			`input[name="username"]`,
			`input[name="user"]`,
			`input[id*="user"]`,
		}

		usernameFilled := false
		for _, selector := range usernameSelectors {
			err = chromedp.Run(ctx,
				chromedp.SendKeys(selector, username, chromedp.ByQuery),
			)
			if err == nil {
				fmt.Printf("用户名填写成功，使用选择器: %s\n", selector)
				usernameFilled = true
				break
			}
		}

		if !usernameFilled {
			fmt.Println("警告: 无法找到用户名输入框")
		}

		// 填写密码
		passwordSelectors := []string{
			`input[id="password"]`,
			`input[placeholder*="密码"]`,
			`input[placeholder*="password" i]`,
			`input[type="password"]`,
			`input[name="password"]`,
			`input[id*="password"]`,
		}

		passwordFilled := false
		for _, selector := range passwordSelectors {
			err = chromedp.Run(ctx,
				chromedp.SendKeys(selector, password, chromedp.ByQuery),
			)
			if err == nil {
				fmt.Printf("密码填写成功，使用选择器: %s\n", selector)
				passwordFilled = true
				break
			}
		}

		if !passwordFilled {
			fmt.Println("警告: 无法找到密码输入框")
			return fmt.Errorf("无法找到密码输入框")
		}

		// 提交表单
		submitSelectors := []string{
			`button.ant-btn.ant-btn-primary`,
			`button[class*="ant-btn-primary"]`,
			`button:contains("登录")`,
			`button:contains("登 录")`,
			`button span:contains("登录")`,
			`button span:contains("登 录")`,
			`input[type="submit"]`,
			`button[type="submit"]`,
		}

		formSubmitted := false
		for _, selector := range submitSelectors {
			err = chromedp.Run(ctx,
				chromedp.Click(selector, chromedp.ByQuery),
			)
			if err == nil {
				fmt.Printf("✓ 表单提交成功，使用选择器: %s\n", selector)
				formSubmitted = true
				break
			} else {
				fmt.Printf("✗ 选择器失败: %v\n", err)
			}
		}

		if !formSubmitted {
			// 尝试通过JavaScript直接点击按钮
			fmt.Println("尝试通过JavaScript点击登录按钮...")
			var jsClickResult bool
			err = chromedp.Run(ctx,
				chromedp.Evaluate(`
					const buttons = document.querySelectorAll('button');
					for (let button of buttons) {
						if (button.textContent.includes('登录') || button.textContent.includes('登 录')) {
							button.click();
							return true;
						}
					}
					return false;
				`, &jsClickResult),
			)
			if err == nil && jsClickResult {
				fmt.Println("✓ JavaScript点击成功")
				formSubmitted = true
			} else {
				fmt.Printf("✗ JavaScript点击失败: %v\n", err)
			}
		}

		if !formSubmitted {
			// 尝试按回车键提交
			fmt.Println("尝试按回车键提交表单...")
			err = chromedp.Run(ctx,
				chromedp.KeyEvent("\r"),
			)
			if err != nil {
				fmt.Printf("按回车键失败: %v\n", err)
			} else {
				fmt.Println("✓ 回车键提交成功")
				formSubmitted = true
			}
		}

		// 等待页面重新加载
		fmt.Println("等待页面重新加载...")
		chromedp.Sleep(3 * time.Second).Do(ctx)
	} else {
		fmt.Println("未找到认证表单，可能页面不需要认证或已经认证")
	}

	return nil
}

// setPagination 设置分页
func setPagination(ctx context.Context) error {
	fmt.Println("正在设置分页为100条/页...")

	// 查找分页选择器
	var hasPagination bool
	err := chromedp.Run(ctx,
		chromedp.Evaluate(`
			document.querySelector('.ant-select-selection-item') !== null ||
			document.querySelector('[title*="条/页"]') !== null ||
			document.querySelector('.ant-pagination-options') !== null
		`, &hasPagination),
	)
	if err != nil {
		fmt.Printf("检查分页选择器失败: %v\n", err)
		return err
	}

	if hasPagination {
		fmt.Println("找到分页选择器，尝试设置为100条/页...")

		// 尝试多种方式点击分页选择器
		paginationSelectors := []string{
			`.ant-select-selection-item`,
			`[title*="条/页"]`,
			`.ant-pagination-options .ant-select`,
			`.ant-pagination-options .ant-select-selector`,
		}

		clicked := false
		for _, selector := range paginationSelectors {
			err = chromedp.Run(ctx,
				chromedp.Click(selector, chromedp.ByQuery),
			)
			if err == nil {
				fmt.Printf("✓ 分页选择器点击成功: %s\n", selector)
				clicked = true
				break
			} else {
				fmt.Printf("✗ 分页选择器失败: %v\n", err)
			}
		}

		if clicked {
			// 等待下拉菜单出现
			chromedp.Sleep(1 * time.Second).Do(ctx)

			// 查找并点击100条/页选项
			optionSelectors := []string{
				`.ant-select-item-option[title="100 条/页"]`,
				`.ant-select-item-option:contains("100")`,
				`.ant-select-dropdown .ant-select-item-option`,
				`[title="100 条/页"]`,
			}

			optionSelected := false
			for _, selector := range optionSelectors {
				err = chromedp.Run(ctx,
					chromedp.Click(selector, chromedp.ByQuery),
				)
				if err == nil {
					fmt.Printf("✓ 100条/页选项选择成功: %s\n", selector)
					optionSelected = true
					break
				} else {
					fmt.Printf("✗ 选项选择器失败: %v\n", err)
				}
			}

			if !optionSelected {
				// 尝试通过JavaScript选择
				fmt.Println("尝试通过JavaScript选择100条/页...")
				var jsResult bool
				err = chromedp.Run(ctx,
					chromedp.Evaluate(`
						const options = document.querySelectorAll('.ant-select-item-option');
						for (let option of options) {
							if (option.textContent.includes('100') && option.textContent.includes('条/页')) {
								option.click();
								return true;
							}
						}
						return false;
					`, &jsResult),
				)
				if err == nil && jsResult {
					fmt.Println("✓ JavaScript选择100条/页成功")
					optionSelected = true
				} else {
					fmt.Printf("✗ JavaScript选择失败: %v\n", err)
				}
			}

			if optionSelected {
				// 等待页面重新加载数据
				fmt.Println("等待页面重新加载数据...")
				chromedp.Sleep(3 * time.Second).Do(ctx)
			}
		} else {
			fmt.Println("无法点击分页选择器，可能已经是100条/页或不需要设置")
		}
	} else {
		fmt.Println("未找到分页选择器，可能页面没有分页功能")
	}

	return nil
}

// mergeScrollScreenshots 合成滚动截图
func mergeScrollScreenshots() error {
	fmt.Println("合成滚动截图...")

	// 查找所有滚动截图文件
	files, err := filepath.Glob("scroll_segment_*.png")
	if err != nil {
		return fmt.Errorf("查找文件失败: %v", err)
	}

	if len(files) == 0 {
		return fmt.Errorf("未找到滚动截图文件")
	}

	// 按文件名排序
	sort.Strings(files)

	fmt.Printf("找到 %d 个截图文件:\n", len(files))
	for i, file := range files {
		fmt.Printf("  %d. %s\n", i+1, file)
	}

	// 读取所有图片
	var images []image.Image
	var totalHeight int

	for _, filename := range files {
		file, err := os.Open(filename)
		if err != nil {
			fmt.Printf("打开文件失败 %s: %v\n", filename, err)
			continue
		}

		img, err := png.Decode(file)
		file.Close()

		if err != nil {
			fmt.Printf("解码图片失败 %s: %v\n", filename, err)
			continue
		}

		images = append(images, img)
		totalHeight += img.Bounds().Dy()
		fmt.Printf("✓ 加载图片: %s (高度: %d)\n", filename, img.Bounds().Dy())
	}

	if len(images) == 0 {
		return fmt.Errorf("没有成功加载任何图片")
	}

	// 创建合成图片
	fmt.Printf("\n开始合成图片...")
	fmt.Printf("总图片数: %d\n", len(images))
	fmt.Printf("总高度: %d 像素\n", totalHeight)

	// 获取最大宽度
	maxWidth := 0
	for _, img := range images {
		if img.Bounds().Dx() > maxWidth {
			maxWidth = img.Bounds().Dx()
		}
	}

	fmt.Printf("最大宽度: %d 像素\n", maxWidth)

	// 创建新的RGBA图片
	mergedImg := image.NewRGBA(image.Rect(0, 0, maxWidth, totalHeight))

	// 将图片垂直拼接
	currentY := 0
	for i, img := range images {
		// 计算居中位置
		offsetX := (maxWidth - img.Bounds().Dx()) / 2

		// 绘制图片
		draw.Draw(mergedImg,
			image.Rect(offsetX, currentY, offsetX+img.Bounds().Dx(), currentY+img.Bounds().Dy()),
			img,
			image.Point{0, 0},
			draw.Src)

		fmt.Printf("✓ 拼接图片 %d: Y=%d-%d\n", i+1, currentY, currentY+img.Bounds().Dy())
		currentY += img.Bounds().Dy()
	}

	// 保存合成图片
	outputFilename := "merged_scroll_screenshot.png"
	file, err := os.Create(outputFilename)
	if err != nil {
		return fmt.Errorf("创建输出文件失败: %v", err)
	}
	defer file.Close()

	err = png.Encode(file, mergedImg)
	if err != nil {
		return fmt.Errorf("保存合成图片失败: %v", err)
	}

	fmt.Printf("\n✅ 合成完成！")
	fmt.Printf("输出文件: %s\n", outputFilename)
	fmt.Printf("最终尺寸: %dx%d 像素\n", maxWidth, totalHeight)

	// 显示文件大小
	fileInfo, _ := os.Stat(outputFilename)
	fmt.Printf("文件大小: %.2f MB\n", float64(fileInfo.Size())/1024/1024)

	return nil
}

// uploadToMinIO 上传文件到MinIO
func uploadToMinIO(filename string) error {
	// 初始化MinIO客户端
	minioClient, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: minioUseSSL,
	})
	if err != nil {
		return fmt.Errorf("创建MinIO客户端失败: %v", err)
	}

	// 检查bucket是否存在，不存在则创建
	ctx := context.Background()
	exists, err := minioClient.BucketExists(ctx, minioBucket)
	if err != nil {
		return fmt.Errorf("检查bucket失败: %v", err)
	}
	if !exists {
		err = minioClient.MakeBucket(ctx, minioBucket, minio.MakeBucketOptions{})
		if err != nil {
			return fmt.Errorf("创建bucket失败: %v", err)
		}
		fmt.Printf("创建bucket: %s\n", minioBucket)
	}

	// 生成带时间戳的文件名
	timestamp := time.Now().Format("20060102_150405")
	objectName := fmt.Sprintf("screenshots/%s_%s", timestamp, filename)

	// 上传文件
	_, err = minioClient.FPutObject(ctx, minioBucket, objectName, filename, minio.PutObjectOptions{
		ContentType: "image/png",
	})
	if err != nil {
		return fmt.Errorf("上传文件失败: %v", err)
	}

	fmt.Printf("文件已上传到: %s/%s\n", minioBucket, objectName)
	return nil
}

// cleanupImages 清理除指定文件外的所有图片文件
func cleanupImages(keepFile string) {
	// 查找所有PNG文件
	files, err := filepath.Glob("*.png")
	if err != nil {
		fmt.Printf("查找图片文件失败: %v\n", err)
		return
	}

	deletedCount := 0
	for _, file := range files {
		if file != keepFile {
			err := os.Remove(file)
			if err != nil {
				fmt.Printf("删除文件失败 %s: %v\n", file, err)
			} else {
				fmt.Printf("✓ 已删除: %s\n", file)
				deletedCount++
			}
		}
	}

	fmt.Printf("清理完成，删除了 %d 个文件，保留了 %s\n", deletedCount, keepFile)
}
