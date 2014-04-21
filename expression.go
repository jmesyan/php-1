package php

import (
	"stephensearles.com/php/ast"
	"stephensearles.com/php/token"
)

var operatorPrecedence = map[token.Token]int{
	token.ArrayLookupOperatorLeft: 19,
	token.UnaryOperator:           18,
	token.BitwiseNotOperator:      18,
	token.CastOperator:            18,
	token.InstanceofOperator:      17,
	token.NegationOperator:        16,
	token.MultOperator:            15,
	token.AdditionOperator:        14,
	token.SubtractionOperator:     14,
	token.ConcatenationOperator:   14,

	token.BitwiseShiftOperator: 13,
	token.ComparisonOperator:   12,
	token.EqualityOperator:     11,

	token.AmpersandOperator:  10,
	token.BitwiseXorOperator: 9,
	token.BitwiseOrOperator:  8,
	token.AndOperator:        7,
	token.OrOperator:         6,
	token.TernaryOperator1:   5,
	token.TernaryOperator2:   5,
	token.AssignmentOperator: 4,
	token.WrittenAndOperator: 3,
	token.WrittenXorOperator: 2,
	token.WrittenOrOperator:  1,
}

func (p *parser) parseExpression() (expr ast.Expression) {
	originalParenLev := p.parenLevel

	switch p.current.typ {
	case token.IgnoreErrorOperator:
		expr = p.parseIgnoreError()
	case token.Function:
		expr = p.parseAnonymousFunction()
	case token.NewOperator:
		expr = p.parseNew(originalParenLev)
	case token.List:
		expr = p.parseList()
	case
		token.UnaryOperator,
		token.NegationOperator,
		token.AmpersandOperator,
		token.CastOperator,
		token.SubtractionOperator,
		token.BitwiseNotOperator:
		op := p.current
		expr = p.parseUnaryExpressionRight(p.parseNextExpression(), op)
	case
		token.VariableOperator,
		token.Array,
		token.Identifier,
		token.StringLiteral,
		token.NumberLiteral,
		token.BooleanLiteral,
		token.Null,
		token.Self,
		token.Static,
		token.Parent,
		token.ShellCommand:
		expr = p.parseOperation(originalParenLev, p.parseOperand())
	case token.Include:
		expr = p.parseInclude()
	case token.OpenParen:
		p.parenLevel += 1
		p.next()
		expr = p.parseExpression()
		p.expect(token.CloseParen)
		p.parenLevel -= 1
		expr = p.parseOperation(originalParenLev, expr)
	default:
		p.errorf("Expected expression. Found %s", p.current)
	}
	if p.parenLevel != originalParenLev {
		p.errorf("unbalanced parens: %d prev: %d", p.parenLevel, originalParenLev)
		return
	}
	return
}

func (p *parser) parseOperation(originalParenLevel int, lhs ast.Expression) (expr ast.Expression) {
	switch p.next(); operationTypeForToken(p.current.typ) {
	case unaryOperation:
		expr = p.parseUnaryExpressionLeft(lhs, p.current)
	case binaryOperation:
		expr = p.parseBinaryOperation(lhs, p.current, originalParenLevel)
	case ternaryOperation:
		expr = p.parseTernaryOperation(lhs)
	case assignmentOperation:
		expr = p.parseAssignmentOperation(lhs)
	default:
		p.backup()
		return lhs
	}
	return p.parseOperation(originalParenLevel, expr)
}

func (p *parser) parseAssignmentOperation(lhs ast.Expression) (expr ast.Expression) {
	assignee, ok := lhs.(ast.Assignable)
	if !ok {
		p.errorf("%s is not assignable", lhs)
	}
	op := p.current.val
	expr = ast.AssignmentExpression{
		Assignee: assignee,
		Operator: op,
		Value:    p.parseNextExpression(),
	}
	return expr
}

// parseOperand takes the current token and returns it as the simplest
// expression for that token. That means an expression with no operators
// except for the object operator.
func (p *parser) parseOperand() (expr ast.Expression) {

	// These cases must come first and not repeat
	switch p.current.typ {
	case
		token.UnaryOperator,
		token.NegationOperator,
		token.CastOperator,
		token.SubtractionOperator,
		token.AmpersandOperator,
		token.BitwiseNotOperator:
		op := p.current
		p.next()
		return p.parseUnaryExpressionRight(p.parseOperand(), op)
	}

	for {
		switch p.current.typ {
		case token.ShellCommand:
			return &ast.ShellCommand{Command: p.current.val}
		case
			token.StringLiteral,
			token.BooleanLiteral,
			token.NumberLiteral,
			token.Null:
			return p.parseLiteral()
		case token.UnaryOperator:
			expr = newUnaryOperation(p.current, expr)
			p.next()
		case token.Array:
			expr = p.parseArrayDeclaration()
			p.next()
		case token.VariableOperator:
			expr = p.parseVariable()
			p.next()
			// Array lookup with curly braces is a special case that is only supported by PHP in
			// simple contexts.
			switch p.current.typ {
			case token.BlockBegin:
				expr = p.parseArrayLookup(expr)
				p.next()
			case token.ScopeResolutionOperator:
				expr = &ast.ClassExpression{Receiver: expr, Expression: p.parseNextExpression()}
				p.next()
			}
		case token.ObjectOperator:
			expr = p.parseObjectLookup(expr)
			p.next()
		case token.ArrayLookupOperatorLeft:
			expr = p.parseArrayLookup(expr)
			p.next()
		case token.Identifier:
			switch p.peek().typ {
			case token.OpenParen:
				// Function calls are okay here because we know they came with
				// a non-dynamic identifier.
				expr = p.parseFunctionCall(ast.Identifier{Value: p.current.val})
				p.next()
				continue
			case token.ScopeResolutionOperator:
				classIdent := p.current.val
				p.next() // get onto ::, then we get to the next expr
				expr = ast.NewClassExpression(classIdent, p.parseNextExpression())
				p.next()
			default:
				expr = ast.ConstantExpression{
					Variable: ast.NewVariable(p.current.val),
				}
				p.next()
			}
		case token.Self, token.Static, token.Parent:
			if p.peek().typ == token.ScopeResolutionOperator {
				r := p.current.val
				p.expect(token.ScopeResolutionOperator)
				expr = ast.NewClassExpression(r, p.parseNextExpression())
				return
			}
			p.next()
		default:
			p.backup()
			return
		}
	}
}

func (p *parser) parseLiteral() *ast.Literal {
	switch p.current.typ {
	case token.StringLiteral:
		return &ast.Literal{Type: ast.String, Value: p.current.val}
	case token.BooleanLiteral:
		return &ast.Literal{Type: ast.Boolean, Value: p.current.val}
	case token.NumberLiteral:
		return &ast.Literal{Type: ast.Float, Value: p.current.val}
	case token.Null:
		return &ast.Literal{Type: ast.Null, Value: p.current.val}
	}
	p.errorf("Unknown literal type")
	return nil
}

func (p *parser) parseVariable() ast.Expression {
	p.expectCurrent(token.VariableOperator)
	switch p.next(); {
	case isKeyword(p.current.typ):
		// keywords are all valid variable names
		fallthrough
	case p.current.typ == token.Identifier:
		expr := ast.NewVariable(p.current.val)
		return expr
	default:
		return p.parseExpression()
	}
}

func (p *parser) parseInclude() ast.Expression {
	inc := ast.Include{Expressions: make([]ast.Expression, 0)}
	for {
		inc.Expressions = append(inc.Expressions, p.parseNextExpression())
		if p.peek().typ != token.Comma {
			break
		}
		p.expect(token.Comma)
	}
	return inc
}

func (p *parser) parseIgnoreError() ast.Expression {
	p.next()
	return p.parseExpression()
}

func (p *parser) parseNew(originalParenLev int) ast.Expression {
	expr := p.parseInstantiation()
	expr = p.parseOperation(originalParenLev, expr)
	return expr
}
